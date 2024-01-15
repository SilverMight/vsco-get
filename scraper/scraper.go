package vsco

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/SilverMight/vsco-get/httpclient"

	"github.com/schollz/progressbar/v3"
)

var client = httpclient.NewClient()

// all we care about is the ID
type sitesResponse struct {
	Sites []struct {
		ID int `json:"id"`
	} `json:"sites"`
}

type imageList struct {
	Media []Media `json:"media"`
	Total int     `json:"total"`
}

type Media struct {
	Is_video       bool   `json:"is_video"`
	Video_url      string `json:"video_url"`
	Responsive_url string `json:"responsive_url"`
	Upload_date    int    `json:"upload_date"`
}

type Scraper struct {
	username   string
	numWorkers int
}

const (
	PAGE_SIZE = 100
)

func NewScraper(username string, numWorkers int) *Scraper {
	return &Scraper{
		username:   username,
		numWorkers: numWorkers,
	}
}

func (scraper *Scraper) getUserId() (int, error) {
	resp, err := client.Get(fmt.Sprintf("https://vsco.co/api/2.0/sites?subdomain=%s", scraper.username))
	if err != nil {
		log.Printf("Failed getting user ID for user %s: %v\n", scraper.username, err)
		return -1, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("Failed to get user ID for user %s: Status %s\n", scraper.username, resp.Status)
	}

	var body sitesResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	if err != nil {
		log.Printf("Failed to decode JSON ID response for user %s: %v\n", scraper.username, err)
		return -1, err
	}

	if len(body.Sites) < 1 {
		return 0, fmt.Errorf("Expected site, got %d", len(body.Sites))
	}

	return body.Sites[0].ID, nil
}

func (scraper *Scraper) fetchImageList() (imageList, error) {
	id, err := scraper.getUserId()
	if err != nil {
		return imageList{}, err
	}

	var list imageList

	for page := 0; ; page++ {
		resp, err := client.Get(fmt.Sprintf("https://vsco.co/api/2.0/medias?site_id=%d&size=%d&page=%d", id, PAGE_SIZE, page))
		if err != nil {
			log.Printf("Failed to get image list for user %s (page %d): %v\n", scraper.username, page, err)
			return imageList{}, err
		}
		defer resp.Body.Close()

		var curPage imageList
		err = json.NewDecoder(resp.Body).Decode(&curPage)
		if err != nil {
			log.Printf("Failed to decode JSON imagelist response for user %s: %v\n", scraper.username, err)
			return imageList{}, err
		}

		list.Media = append(list.Media, curPage.Media...)
		list.Total += curPage.Total

		// No more new pages
		if len(curPage.Media) < PAGE_SIZE {
			break
		}
	}

	return list, nil
}

// vsco returns us links that doesn't have https:// in front of it
func fixUrl(rawUrl string) (fixedUrl string) {
	if strings.HasPrefix(rawUrl, "https://") {
		return rawUrl
	}
	return "https://" + rawUrl
}

func getCorrectUrl(media Media) (url string) {
	if media.Is_video {
		return media.Video_url
	}
	return media.Responsive_url
}

func SaveMediaToFile(media Media, folderPath string) error {
	// Determine if we're saving an image or video
	mediaUrl := getCorrectUrl(media)
	mediaUrl = fixUrl(mediaUrl)

	parsed, err := url.Parse(mediaUrl)
	if err != nil {
		log.Printf("Failed to parse image URL for media %s: %v\n", media.Responsive_url, err)
		return err
	}

	imageFile := path.Base(parsed.Path)

	imagePath := path.Join(folderPath, imageFile)

	// Don't save image if it already exists
	if _, doesNotExist := os.Stat(imagePath); doesNotExist == nil {
		return nil
	}

	err = client.DownloadFile(mediaUrl, imagePath)
	if err != nil {
		log.Printf("Failed to download image %s: %v\n", mediaUrl, err)
		return err
	}

	// We care about the modification time
	imageTime := time.Unix(int64(media.Upload_date)/int64(1000), 0)
	os.Chtimes(imagePath, imageTime, imageTime)

	return nil
}

func (scraper *Scraper) SaveAllMedia() error {
	imagelist, err := scraper.fetchImageList()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("Could not get cwd: %v\n", err)
		return err
	}

	userPath := path.Join(cwd, scraper.username)

	err = os.MkdirAll(userPath, 0755)

	if err != nil {
		log.Printf("Could not create directory %s: %v\n", userPath, err)
		return err
	}

	// Dumb concurrency
	var sem = make(chan int, scraper.numWorkers)
	var wg sync.WaitGroup

	bar := progressbar.Default(int64(len(imagelist.Media)), fmt.Sprintf("Downloading images from %s...", scraper.username))
	for _, media := range imagelist.Media {
		sem <- 1
		wg.Add(1)
		go func(media Media) {
			defer func() {
				<-sem
				wg.Done()
				bar.Add(1)
			}()

			err := SaveMediaToFile(media, userPath)
			// Keeps going and logs if one fails (maybe make threshold of failures)
			if err != nil {
				log.Printf("Error downloading %v\n", err)
			}
		}(media)
	}

	wg.Wait()

	return nil
}

func GetMediaFromUserlist(list string, numWorkers int) error {
	file, err := os.Open(list)
	if err != nil {
		log.Printf("Failed to open file %s: %v\n", list, err)
		return err
	}

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		scraper := NewScraper(scanner.Text(), numWorkers)

		err := scraper.SaveAllMedia()
		if err != nil {
			log.Printf("Failed to get images for user %s: %v\n", scraper.SaveAllMedia(), err)
		}
	}

	return nil
}
