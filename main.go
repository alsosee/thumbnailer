// A go app to handle media, thumbnails, blurhashes, etc.
// It will
// - generate thumbnails for all images in the directories
// - upload media and thumbnails to R2 Cloudflare storage if they don't exist yet
// - generate BlurHash for each photo
// - keep track of them in a .thumbs.yaml file that will be used by the `finder` to generate the website
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
	flags "github.com/jessevdk/go-flags"
	gitignore "github.com/sabhiram/go-gitignore"
	"gopkg.in/yaml.v3"

	"github.com/alsosee/thumbnailer/pkg/thumbnailer"
)

type appConfig struct {
	// Directory with media files
	MediaDir string `env:"INPUT_MEDIA" long:"media-dir" description:"path to media directory" default:"media"`

	// Cloudflare R2 storage
	R2AccountID       string `env:"INPUT_R2_ACCOUNT_ID" long:"r2-account-id" description:"r2 account id"`
	R2AccessKeyID     string `env:"INPUT_R2_ACCESS_KEY_ID" long:"r2-access-key-id" description:"r2 access key id"`
	R2AccessKeySecret string `env:"INPUT_R2_ACCESS_KEY_SECRET" long:"r2-access-key-secret" description:"r2 access key secret"`
	R2Bucket          string `env:"INPUT_R2_BUCKET" long:"r2-bucket" description:"r2 bucket"`

	// Force thumbnail generation
	ForceThumbnails bool `env:"INPUT_FORCE_THUMBNAILS" long:"force-thumbnails" description:"force thumbnail generation"`

	Include []string `env:"INPUT_INCLUDE" long:"include" description:"include only these directories"`

	SkipImageUpload bool `env:"INPUT_SKIP_IMAGE_UPLOAD" long:"skip-image-upload" description:"skip image upload to R2"`

	// Blurhash
	ForceBlurhash       bool `env:"INPUT_FORCE_BLURHASH" long:"force-blurhash" description:"force blurhash generation"`
	ForceBlurhashImages bool `env:"INPUT_FORCE_BLURHASH_IMAGES" long:"force-blurhash-images" description:"force blurhash images generation"`
}

var cfg appConfig

func main() {
	log.Info("Starting...")

	if err := run(); err != nil {
		log.Fatal(err)
	}

	log.Info("Finished")
}

func run() error {
	_, err := flags.Parse(&cfg)
	if err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	ctx := context.Background()

	r2, err := NewR2(
		cfg.R2AccountID,
		cfg.R2AccessKeyID,
		cfg.R2AccessKeySecret,
		cfg.R2Bucket,
	)
	if err != nil {
		return fmt.Errorf("creating R2 client: %w", err)
	}

	dirs, err := scanDirectories(cfg.MediaDir)
	if err != nil {
		return fmt.Errorf("scanning directories: %w", err)
	}

	for _, dir := range dirs {
		err = processDirectory(ctx, r2, dir)
		if err != nil {
			return fmt.Errorf("processing directory %q: %w", dir, err)
		}
	}

	return nil
}

func scanDirectories(dir string) ([]string, error) {
	var result []string

	// filter empty strings from cfg.Include
	var include []string
	for _, item := range cfg.Include {
		if item != "" {
			include = append(include, item)
		}
	}
	gi := gitignore.CompileIgnoreLines(include...)

	log.Info("Getting directories...")
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() { // skip files
			return nil
		}

		// temporary: if Dir name includes "_ignore", skip it
		if strings.Contains(info.Name(), "_ignore") {
			return filepath.SkipDir
		}

		// Skip .git directory
		if info.Name() == ".git" || info.Name() == ".github" {
			return filepath.SkipDir
		}

		if len(include) > 0 && !gi.MatchesPath(path) {
			log.Infof("Ignoring %s", path)
			return nil
		}

		result = append(result, path)
		return nil
	})
	return result, err
}

func processDirectory(ctx context.Context, r2 *R2, dir string) error {
	log.Infof("Processing %s", dir)

	thumbsFile := filepath.Join(dir, ".thumbs.yml")

	// look for .thumb.yml file
	media, err := thumbnailer.LoadThumbsFile(thumbsFile)
	if err != nil && !errors.Is(err, thumbnailer.ErrThumbYamlNotFound) {
		return fmt.Errorf("loading thumbs file: %w", err)
	}

	// scan directory for all image files
	files, err := thumbnailer.ScanDirectory(dir)
	if err != nil {
		return fmt.Errorf("scanning directory: %w", err)
	}

	media, err = uploadNewMedia(ctx, r2, media, files, dir)
	if err != nil {
		return fmt.Errorf("uploading new media: %w", err)
	}

	mediaGrouped := groupByType(media)

	for format, media := range mediaGrouped {
		_, err = generateThumbnails(ctx, r2, media, dir, format, cfg.ForceThumbnails)
		if err != nil {
			return fmt.Errorf("generating thumbnails: %w", err)
		}
	}

	if err = saveThumbsFile(thumbsFile, media); err != nil {
		return fmt.Errorf("saving media: %w", err)
	}

	return nil
}

func diff(media []*thumbnailer.Media, files []string) (toAdd, toDelete []string) {
	// find new files
	for _, file := range files {
		if !containsMedia(media, file) {
			toAdd = append(toAdd, file)
		}
	}

	// find deleted files
	for _, file := range media {
		if !contains(files, file.Path) {
			toDelete = append(toDelete, file.Path)
		}
	}

	return toAdd, toDelete
}

func containsMedia(arr []*thumbnailer.Media, needle string) bool {
	for _, item := range arr {
		if item.Path == needle {
			return true
		}
	}

	return false
}

func saveThumbsFile(path string, media []*thumbnailer.Media) error {
	if len(media) == 0 {
		return nil
	}

	fileContent, err := yaml.Marshal(media)
	if err != nil {
		return fmt.Errorf("marshaling media: %w", err)
	}

	if err = os.WriteFile(path, fileContent, 0o644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	return nil
}

func uploadNewMedia(
	ctx context.Context,
	r2 *R2,
	media []*thumbnailer.Media,
	files []string,
	dir string,
) ([]*thumbnailer.Media, error) {
	toAdd, toDelete := diff(media, files)

	for _, file := range toAdd {
		media = append(media, &thumbnailer.Media{
			Path: file,
		})

		if !cfg.SkipImageUpload {
			path := filepath.Join(dir, file)
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("reading file: %w", err)
			}

			// R2 object key is the same as file path, relative to media directory
			key := strings.TrimPrefix(path, cfg.MediaDir+"/")

			log.Infof("Uploading %s", key)
			if err = r2.Upload(ctx, key, content); err != nil {
				return nil, fmt.Errorf("uploading file: %w", err)
			}
		}
	}

	for _, file := range toDelete {
		for i, existing := range media {
			if existing.Path == file {
				// todo: delete from r2

				media = append(media[:i], media[i+1:]...)
				break
			}
		}
	}

	return media, nil
}

func groupByType(media []*thumbnailer.Media) map[string][]*thumbnailer.Media {
	result := make(map[string][]*thumbnailer.Media)

	for _, file := range media {
		ext := strings.Trim(filepath.Ext(file.Path), ".")
		if ext == "jpeg" {
			ext = "jpg"
		}

		if _, ok := result[ext]; !ok {
			result[ext] = make([]*thumbnailer.Media, 0)
		}

		result[ext] = append(result[ext], file)
	}

	return result
}

const (
	maxPerRow = 10
	maxRows   = 5
)

func generateThumbnails(
	ctx context.Context,
	r2 *R2,
	media []*thumbnailer.Media,
	dir string,
	format string,
	force bool,
) ([]*thumbnailer.Media, error) {
	// split files into batches of 100 files each
	batches := make([][]*thumbnailer.Media, 0)
	for i := 0; i < len(media); i += maxPerRow * maxRows {
		end := i + maxPerRow*maxRows
		if end > len(media) {
			end = len(media)
		}
		batches = append(batches, media[i:end])
	}

	// filter out batches if all files in it already have thumbnails
	if !force {
		for batch, files := range batches {
			allHaveThumbs := true
			allHaveSameThumb := true
			for _, file := range files {
				if file.ThumbPath == "" {
					log.Infof("Batch %d has no thumbnails", batch)
					allHaveThumbs = false
					break
				}
				if file.ThumbPath != files[0].ThumbPath {
					log.Infof("Batch %d has different ThumbPath: want %q, have %q", batch, file.ThumbPath, files[0].ThumbPath)
					allHaveSameThumb = false
					break
				}
			}
			if allHaveThumbs && allHaveSameThumb {
				batches[batch] = nil
			}
		}
	} else {
		log.Info("Forcing thumbnail generation")
	}

	// generate thumbnails for each year
	for batch, files := range batches {
		if files == nil {
			continue
		}

		thumbPath := fmt.Sprintf("thumbnails_%d.%s", batch, format)

		log.Infof("Generating %s thumbnail for batch %d in %s", format, batch, dir)
		b, err := thumbnailer.GenerateThumbnail(files, dir, format)
		if err != nil {
			return nil, fmt.Errorf("generating thumbnail for %s / %d: %w", dir, batch, err)
		}

		// update thumb path with CRC32 checksum for each photo
		for _, photo := range media {
			log.Infof("Updating thumb path for %s", photo.Path)
			photo.ThumbPath = thumbPath + "?crc=" + crc32sum(b)
		}

		err = os.WriteFile(filepath.Join(dir, thumbPath), b, 0o644)
		if err != nil {
			return nil, fmt.Errorf("writing thumbnail %q: %w", thumbPath, err)
		}

		// upload thumbnail to R2
		if !cfg.SkipImageUpload {
			cleanPath := strings.TrimPrefix(filepath.Join(dir, thumbPath), cfg.MediaDir+"/")
			if err := r2.Upload(ctx, cleanPath, b); err != nil {
				return nil, fmt.Errorf("uploading thumbnail %q: %w", thumbPath, err)
			}
		} else {
			log.Infof("Skipping thumbnail upload")
		}
	}

	return media, nil
}

func crc32sum(content []byte) string {
	hash := crc32.NewIEEE()
	if _, err := io.Copy(hash, bytes.NewReader(content)); err != nil {
		log.Errorf("error calculating CRC32 checksum: %v", err)
		return ""
	}

	return fmt.Sprintf("%x", hash.Sum32())
}

func contains(arr []string, needle string) bool {
	for _, item := range arr {
		if item == needle {
			return true
		}
	}

	return false
}
