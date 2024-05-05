package thumbnailer

import (
	"fmt"
	"image"
	"os"

	"gopkg.in/yaml.v3"
)

var ErrThumbYamlNotFound = fmt.Errorf(".thumbs.yml not found")

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
	Image image.Image `yaml:"-"`
}

// MediaContainer is a wrapper for Photo struct, used for sorting,
// so that references are not swapped and still can be modified.
type MediaContainer struct {
	Media *Media
}

type ByThumbHeightDesc []MediaContainer

func (a ByThumbHeightDesc) Len() int      { return len(a) }
func (a ByThumbHeightDesc) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a ByThumbHeightDesc) Less(i, j int) bool {
	return a[i].Media.ThumbHeight > a[j].Media.ThumbHeight
}

func LoadThumbsFile(path string) ([]*Media, error) {
	// check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, ErrThumbYamlNotFound
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
