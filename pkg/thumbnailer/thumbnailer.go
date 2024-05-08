package thumbnailer

import (
	"bytes"
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
	"strings"

	"github.com/charmbracelet/log"
	"github.com/nfnt/resize"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	maxThumbSize = 324 /* 162 * 2 */
	maxPerRow    = 10
	maxRows      = 5
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
	image image.Image `yaml:"-"`
}

type Uploader interface {
	Upload(key string, body []byte) error
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

func SaveThumbsFile(path string, media []*Media) error {
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

func ProcessDirectory(dir string, up Uploader, force bool) error {
	log.Infof("Processing %s", dir)

	thumbsFile := filepath.Join(dir, ".thumbs.yml")

	// look for .thumb.yml file
	media, err := LoadThumbsFile(thumbsFile)
	if err != nil && !errors.Is(err, ErrThumbYamlNotFound) {
		return fmt.Errorf("loading thumbs file: %w", err)
	}

	// scan directory for all image files
	files, err := ScanDirectory(dir)
	if err != nil {
		return fmt.Errorf("scanning directory: %w", err)
	}

	media, err = UploadNewMedia(up, media, files, dir)
	if err != nil {
		return fmt.Errorf("uploading new media: %w", err)
	}

	mediaGrouped := groupByType(media)

	for format, media := range mediaGrouped {
		_, err = GenerateThumbnails(up, media, dir, format, force)
		if err != nil {
			return fmt.Errorf("generating thumbnails: %w", err)
		}
	}

	if err = SaveThumbsFile(thumbsFile, media); err != nil {
		return fmt.Errorf("saving media: %w", err)
	}

	return nil
}

func UploadNewMedia(
	uploader Uploader,
	media []*Media,
	files []string,
	dir string,
) ([]*Media, error) {
	toAdd, toDelete := diff(media, files)

	for _, file := range toAdd {
		media = append(media, &Media{
			Path: file,
		})

		path := filepath.Join(dir, file)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading file: %w", err)
		}

		if err = uploader.Upload(path, content); err != nil {
			return nil, fmt.Errorf("uploading file: %w", err)
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

func GenerateThumbnails(
	uploader Uploader,
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

		thumbPath := fmt.Sprintf("thumbnails_%d.%s", batch, format)

		log.Infof("Generating %s thumbnail for batch %d in %s", format, batch, dir)
		b, err := GenerateThumbnail(files, dir, format)
		if err != nil {
			return nil, fmt.Errorf("generating thumbnail for %s / %d: %w", dir, batch, err)
		}

		// update thumb path with CRC32 checksum for each photo
		for _, file := range files {
			log.Infof("Updating thumb path for %s", file.Path)
			file.ThumbPath = thumbPath + "?crc=" + crc32sum(b)
		}

		err = os.WriteFile(filepath.Join(dir, thumbPath), b, 0o644)
		if err != nil {
			return nil, fmt.Errorf("writing thumbnail %q: %w", thumbPath, err)
		}

		// upload thumbnail to R2
		if err := uploader.Upload(filepath.Join(dir, thumbPath), b); err != nil {
			return nil, fmt.Errorf("uploading thumbnail %q: %w", thumbPath, err)
		}
	}

	return media, nil
}

func GenerateThumbnail(media []*Media, dir, format string) ([]byte, error) {
	// each thumbnail should fit into 140x140px square, maximum 10 files in a row
	for _, file := range media {
		// decode photo
		img, err := readImage(dir, file.Path)
		if err != nil {
			return nil, fmt.Errorf("reading image: %w", err)
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

	var b bytes.Buffer
	switch format {
	case "png":
		// encode thumbnail into PNG
		if err := png.Encode(&b, img); err != nil {
			return nil, fmt.Errorf("encoding thumbnail: %w", err)
		}
	case "jpg":
		jpegOptions := jpeg.Options{
			Quality: 95,
		}
		if err := jpeg.Encode(&b, img, &jpegOptions); err != nil {
			return nil, fmt.Errorf("encoding thumbnail: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}

	return b.Bytes(), nil
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

func containsMedia(arr []*Media, needle string) bool {
	for _, item := range arr {
		if item.Path == needle {
			return true
		}
	}

	return false
}
