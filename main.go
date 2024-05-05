// A go app to handle media, thumbnails, blurhashes, etc.
// It will
// - generate thumbnails for all images in the directories
// - upload media and thumbnails to R2 Cloudflare storage if they don't exist yet
// - generate BlurHash for each photo
// - keep track of them in a .thumbs.yaml file that will be used by the `finder` to generate the website
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
	flags "github.com/jessevdk/go-flags"
	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/alsosee/thumbnailer/pkg/r2"
	"github.com/alsosee/thumbnailer/pkg/thumbnailer"
	"github.com/alsosee/thumbnailer/pkg/uploader"
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

	var up thumbnailer.Uploader
	if cfg.SkipImageUpload {
		up = uploader.NewNoOp()
	} else {
		r2, err := r2.NewR2(
			cfg.R2AccountID,
			cfg.R2AccessKeyID,
			cfg.R2AccessKeySecret,
			cfg.R2Bucket,
		)
		if err != nil {
			return fmt.Errorf("creating R2 client: %w", err)
		}
		up = uploader.NewR2(
			context.Background(),
			r2,
			cfg.MediaDir+"/",
		)
	}

	dirs, err := scanDirectories(cfg.MediaDir)
	if err != nil {
		return fmt.Errorf("scanning directories: %w", err)
	}

	for _, dir := range dirs {
		err = thumbnailer.ProcessDirectory(dir, up, cfg.ForceThumbnails)
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
