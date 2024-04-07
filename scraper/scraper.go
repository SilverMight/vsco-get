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
		ID            int    `json:"id"`
		Profile_image string `json:"profile_image"`
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
	username     string
	numWorkers   int
	id           int
	profileImage string
}

const (
	PageSize = 100
)

func NewScraper(username string, numWorkers int) *Scraper {
	return &Scraper{
		username:   username,
		numWorkers: numWorkers,
	}
}

func (scraper *Scraper) GetUserInfo() error {
	resp, err := client.Get(fmt.Sprintf("https://vsco.co/api/2.0/sites?subdomain=%s", scraper.username))
	if err != nil {
		log.Printf("Failed getting user info for user %s: %v\n", scraper.username, err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Failed to get user info for user %s: Status %s\n", scraper.username, resp.Status)
	}

	var body sitesResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	if err != nil {
		log.Printf("Failed to decode JSON response for user info %s: %v\n", scraper.username, err)
		return err
	}

	if len(body.Sites) < 1 {
		return fmt.Errorf("expected site, got %d", len(body.Sites))
	}

	scraper.id = body.Sites[0].ID
	scraper.profileImage = body.Sites[0].Profile_image

	return nil
}

func (scraper *Scraper) fetchImageList() (imageList, error) {
	var list imageList

	for page := 0; ; page++ {
		resp, err := client.Get(fmt.Sprintf("https://vsco.co/api/2.0/medias?site_id=%d&size=%d&page=%d", scraper.id, PageSize, page))
		if err != nil {
			log.Printf("Failed to get image list for user %s (page %d): %v\n", scraper.username, page, err)
			return imageList{}, err
		}

		var curPage imageList
		err = json.NewDecoder(resp.Body).Decode(&curPage)
		resp.Body.Close()

		if err != nil {
			log.Printf("Failed to decode JSON imagelist response for user %s: %v\n", scraper.username, err)
			return imageList{}, err
		}

		list.Media = append(list.Media, curPage.Media...)
		list.Total += curPage.Total

		// No more new pages
		if len(curPage.Media) < PageSize {
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

func getMediaFilename(media Media) (string, error) {
	mediaUrl := getCorrectUrl(media)

	parsed, err := url.Parse(mediaUrl)
	if err != nil {
		log.Printf("Failed to parse image URL for media %s: %v\n", media.Responsive_url, err)
		return "", err
	}

	return path.Base(parsed.Path), nil
}

func SaveMediaToFile(media Media, folderPath string) error {
	// Determine if we're saving an image or video
	mediaUrl := getCorrectUrl(media)
	mediaUrl = fixUrl(mediaUrl)

	imageFile, err := getMediaFilename(media)
	if err != nil {
		return err
	}

	imagePath := path.Join(folderPath, imageFile)

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

func stripExistingMedia(mediaList imageList, userPath string) (imageList, error) {
	var strippedList imageList

	for _, media := range mediaList.Media {
		mediaFilename, err := getMediaFilename(media)

		if err != nil {
			return imageList{}, err
		}
		if _, exists := os.Stat(path.Join(userPath, mediaFilename)); exists != nil {
			strippedList.Media = append(strippedList.Media, media)
		}
	}

	return strippedList, nil
}

func createUserDirectory(username string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("Could not get cwd: %v\n", err)
		return "", err
	}

	userPath := path.Join(cwd, username)

	err = os.MkdirAll(userPath, 0755)

	if err != nil {
		log.Printf("Could not create directory %s: %v\n", userPath, err)
		return "", err
	}

	return userPath, nil
}

func (scraper *Scraper) SaveAllMedia() error {
	imagelist, err := scraper.fetchImageList()
	if err != nil {
		return err
	}

	userPath, err := createUserDirectory(scraper.username)
	if err != nil {
		return err
	}

	// Strip our list so we don't save duplicates
	imagelist, err = stripExistingMedia(imagelist, userPath)
	if err != nil {
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
		return fmt.Errorf("Failed to open file %s: %v\n", list, err)
	}

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		scraper := NewScraper(scanner.Text(), numWorkers)
		err := scraper.GetUserInfo()
		if err != nil {
			return err
		}

		err = scraper.SaveAllMedia()
	}

	return nil
}

func (scraper *Scraper) SaveProfilePicture() error {
	userPath, err := createUserDirectory(scraper.username)
	if err != nil {
		return err
	}

	profileFolder := path.Join(userPath, "profile")

	err = os.MkdirAll(profileFolder, 0755)
	if err != nil {
		log.Printf("Could not create directory %s: %v\n", profileFolder, err)
		return err
	}

	err = client.DownloadFile(scraper.profileImage, path.Join(profileFolder, fmt.Sprintf("%s.jpg", scraper.username)))
	if err != nil {
		log.Printf("Failed to download profile picture %s: %v\n", scraper.profileImage, err)
		return err
	}

	return nil
}
