package thumbnailer

import (
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/nfnt/resize"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	maxThumbSize = 324 /* 162 * 2 */
	maxPerRow    = 10
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

func ScanDirectory(dir string) ([]string, error) {
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

		result = append(result, fixUnicode(file.Name()))
	}

	sort.Strings(result)

	return result, nil
}

func GenerateThumbnail(batch int, media []*Media, dir, format string) (string, error) {
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
		file.Image = img
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
	sort.Sort(ByThumbHeightDesc(containers))

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
			container.Media.Image,
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

func contains(arr []string, needle string) bool {
	for _, item := range arr {
		if item == needle {
			return true
		}
	}

	return false
}

func fixUnicode(in string) string {
	return norm.NFC.String(in)
}
