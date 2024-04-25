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
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"
	flags "github.com/jessevdk/go-flags"
	"github.com/nfnt/resize"
	gitignore "github.com/sabhiram/go-gitignore"
	"gopkg.in/yaml.v3"
)

// Media struct for items in .thumbs.yml file.
type Media struct {
	Path                string
	Width               int    `yaml:"width,omitempty"`
	Height              int    `yaml:"height,omitempty"`
	ThumbPath           string `yaml:"thumb,omitempty"`
	ThumbXOffset        int    `yaml:"thumb_x,omitempty"`
	ThumbYOffset        int    `yaml:"thumb_y,omitempty"`
	ThumbWidth          int    `yaml:"thumb_width,omitempty"`
	ThumbHeight         int    `yaml:"thumb_height,omitempty"`
	ThumbTotalWidth     int    `yaml:"thumb_total_width,omitempty"`
	ThumbTotalHeight    int    `yaml:"thumb_total_height,omitempty"`
	Blurhash            string `yaml:"blurhash,omitempty"`
	BlurhashImageBase64 string `yaml:"blurhash_image_base64,omitempty"`

	// Temporary image.Image field used to generate thumbnails
	image image.Image `yaml:"-"`
}

// MediaContainer is a wrapper for Photo struct, used for sorting,
// so that references are not swapped and still can be modified.
type MediaContainer struct {
	Media *Media
}

type byThumbHeightDesc []MediaContainer

func (a byThumbHeightDesc) Len() int      { return len(a) }
func (a byThumbHeightDesc) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byThumbHeightDesc) Less(i, j int) bool {
	return a[i].Media.ThumbHeight > a[j].Media.ThumbHeight
}

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

var errThumbYamlNotFound = fmt.Errorf(".thumbs.yml not found")

func processDirectory(ctx context.Context, r2 *R2, dir string) error {
	log.Infof("Processing %s", dir)

	thumbsFile := filepath.Join(dir, ".thumbs.yml")

	// look for .thumb.yml file
	media, err := loadThumbsFile(thumbsFile)
	if err != nil && !errors.Is(err, errThumbYamlNotFound) {
		return fmt.Errorf("loading thumbs file: %w", err)
	}

	// scan directory for all image files
	files, err := scanDirectory(dir)
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

func loadThumbsFile(path string) ([]*Media, error) {
	// check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, errThumbYamlNotFound
	}

	// read .thumbs.yml file
	fileContent, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	var media []*Media
	if err = yaml.Unmarshal(fileContent, &media); err != nil {
		return nil, fmt.Errorf("unmarshaling file: %w", err)
	}

	return media, nil
}

func scanDirectory(dir string) ([]string, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory %q: %w", dir, err)
	}

	var result []string
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		if strings.HasPrefix(file.Name(), "thumbnails_") {
			continue
		}

		ext := filepath.Ext(file.Name())
		if !contains([]string{".jpg", ".jpeg", ".png"}, ext) {
			continue
		}

		result = append(result, file.Name())
	}

	sort.Strings(result)

	return result, nil
}

func diff(media []*Media, files []string) (toAdd, toDelete []string) {
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

func contains(arr []string, needle string) bool {
	for _, item := range arr {
		if item == needle {
			return true
		}
	}

	return false
}

func containsMedia(arr []*Media, needle string) bool {
	for _, item := range arr {
		if item.Path == needle {
			return true
		}
	}

	return false
}

func saveThumbsFile(path string, media []*Media) error {
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
	media []*Media,
	files []string,
	dir string,
) ([]*Media, error) {
	toAdd, toDelete := diff(media, files)

	for _, file := range toAdd {
		media = append(media, &Media{
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

func groupByType(media []*Media) map[string][]*Media {
	result := make(map[string][]*Media)

	for _, file := range media {
		ext := strings.Trim(filepath.Ext(file.Path), ".")
		if ext == "jpeg" {
			ext = "jpg"
		}

		if _, ok := result[ext]; !ok {
			result[ext] = make([]*Media, 0)
		}

		result[ext] = append(result[ext], file)
	}

	return result
}

const (
	maxThumbSize = 324 /* 162 * 2 */
	maxPerRow    = 10
	maxRows      = 5
)

func generateThumbnails(
	ctx context.Context,
	r2 *R2,
	media []*Media,
	dir string,
	format string,
	force bool,
) ([]*Media, error) {
	// split files into batches of 100 files each
	batches := make([][]*Media, 0)
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

		thumbPath, err := generateThumbnail(batch, files, dir, format)
		if err != nil {
			return nil, fmt.Errorf("generating thumbnail for %s / %d: %w", dir, batch, err)
		}

		// upload thumbnail to R2
		thumbContent, err := os.ReadFile(filepath.Join(dir, thumbPath))
		if err != nil {
			return nil, fmt.Errorf("reading thumbnail %q: %w", thumbPath, err)
		}

		cleanPath := strings.TrimPrefix(filepath.Join(dir, thumbPath), cfg.MediaDir+"/")

		if err := r2.Upload(ctx, cleanPath, thumbContent); err != nil {
			return nil, fmt.Errorf("uploading thumbnail %q: %w", thumbPath, err)
		}

		// update thumb path with CRC32 checksum for each photo
		for _, photo := range media {
			photo.ThumbPath = thumbPath + "?crc=" + crc32sum(thumbContent)
		}
	}

	return media, nil
}

func generateThumbnail(batch int, media []*Media, dir, format string) (string, error) {
	log.Infof("Generating %s thumbnail for batch %d in %s", format, batch, dir)
	// each thumbnail should fit into 140x140px square, maximum 10 files in a row
	for _, file := range media {
		// decode photo
		img, err := readImage(dir, file.Path)
		if err != nil {
			return "", fmt.Errorf("reading image: %w", err)
		}
		file.Width = img.Bounds().Dx()
		file.Height = img.Bounds().Dy()

		// resize photo to 140x140px
		img = resize.Thumbnail(
			maxThumbSize,
			maxThumbSize,
			img,
			resize.Lanczos3,
		)
		file.image = img
		file.ThumbWidth = img.Bounds().Dx()
		file.ThumbHeight = img.Bounds().Dy()
	}

	// sort media by height, aiming to have less empty space
	// create a slice of pointers to the original files
	containers := make([]MediaContainer, len(media))
	for i := range media {
		containers[i].Media = media[i]
	}

	// sort the slice of pointers by thumb height in descending order
	sort.Sort(byThumbHeightDesc(containers))

	// calculate thumbnail image size
	var (
		rowWidth    int
		totalWidth  int
		totalHeight int
		counter     int
	)
	for i, container := range containers {
		if i == 0 {
			totalHeight = container.Media.ThumbHeight
			totalWidth = container.Media.ThumbWidth
		}

		if counter == maxPerRow {
			totalHeight += container.Media.ThumbHeight
			if rowWidth > totalWidth {
				totalWidth = rowWidth
			}
			rowWidth = 0
			counter = 0
		}

		rowWidth += container.Media.ThumbWidth
		counter++
	}

	if rowWidth > totalWidth {
		totalWidth = rowWidth
	}

	img := image.NewRGBA(image.Rect(0, 0, totalWidth, totalHeight))

	// draw files on thumbnail
	var (
		thumbPath = "thumbnails_" + strconv.Itoa(batch) + "." + format
		x         int
		y         int
		col       int
		rowHeight int
	)

	for i, container := range containers {
		if i == 0 {
			rowHeight = container.Media.ThumbHeight
		}

		if col == maxPerRow {
			x = 0
			col = 0
			y += rowHeight
			rowHeight = container.Media.ThumbHeight
		}

		container.Media.ThumbPath = thumbPath
		container.Media.ThumbXOffset = x
		container.Media.ThumbYOffset = y
		container.Media.ThumbTotalWidth = totalWidth
		container.Media.ThumbTotalHeight = totalHeight

		draw.Draw(
			img,
			image.Rect(x, y, x+container.Media.ThumbWidth, y+container.Media.ThumbHeight),
			container.Media.image,
			image.Point{0, 0},
			draw.Src,
		)
		x += container.Media.ThumbWidth
		col++
	}

	out, err := os.Create(filepath.Join(dir, thumbPath))
	if err != nil {
		return "", fmt.Errorf("creating file %q: %w", thumbPath, err)
	}
	defer out.Close()

	switch format {
	case "png":
		// encode thumbnail into PNG
		if err = png.Encode(out, img); err != nil {
			return "", fmt.Errorf("encoding thumbnail: %w", err)
		}
	case "jpg":
		jpegOptions := jpeg.Options{
			Quality: 95,
		}
		if err := jpeg.Encode(out, img, &jpegOptions); err != nil {
			return "", fmt.Errorf("encoding thumbnail: %w", err)
		}
	default:
		return "", fmt.Errorf("unsupported format: %s", format)
	}

	return thumbPath, nil
}

func readImage(dir, path string) (image.Image, error) {
	file, err := os.Open(filepath.Join(dir, path))
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}

	return img, nil
}

func crc32sum(content []byte) string {
	hash := crc32.NewIEEE()
	if _, err := io.Copy(hash, bytes.NewReader(content)); err != nil {
		log.Errorf("error calculating CRC32 checksum: %v", err)
		return ""
	}

	return fmt.Sprintf("%x", hash.Sum32())
}
