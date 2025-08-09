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
	"strconv"
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
	Media       []MediaWrapper `json:"media"`
	Total       int            `json:"total"`
	Next_cursor string         `json:"next_cursor"`
}

type MediaWrapper struct {
	Type  string `json:"type"`
	Media Media  `json:"-"`
}

type Media struct {
	Is_video       bool   `json:"is_video"`
	Video_url      string `json:"video_url"`
	Responsive_url string `json:"responsive_url"`
	Upload_date    int    `json:"upload_date"`
}

func (mw *MediaWrapper) UnmarshalJSON(data []byte) error {
	var rawJson map[string]json.RawMessage

	if err := json.Unmarshal(data, &rawJson); err != nil {
		return err
	}

	if typeBytes, exists := rawJson["type"]; exists {
		if err := json.Unmarshal(typeBytes, &mw.Type); err != nil {
			return err
		}
	}

	mediaBytes, exists := rawJson[mw.Type]

	if !exists {
		return fmt.Errorf("Missing key matching media type: %s", mw.Type)
	}

	if err := json.Unmarshal(mediaBytes, &mw.Media); err != nil {
		return fmt.Errorf("Failed to unmarshal media of type %s: %w", mw.Type, err)
	}

	return nil
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
		return fmt.Errorf("Failed getting user info for user %s: %w\n", scraper.username, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Failed to get user info for user %s: Status %s\n", scraper.username, resp.Status)
	}

	var body sitesResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	if err != nil {
		return fmt.Errorf("Failed to decode JSON response for user info %s: %w\n", scraper.username, err)
	}

	if len(body.Sites) < 1 {
		return fmt.Errorf("Expected site, got %d", len(body.Sites))
	}

	scraper.id = body.Sites[0].ID
	scraper.profileImage = body.Sites[0].Profile_image

	return nil
}

func (scraper *Scraper) fetchImageList() ([]Media, error) {
	var list []Media

	nextCursor := ""
	for page := 1; ; page++ {
		url := fmt.Sprintf("https://vsco.co/api/3.0/medias/profile?site_id=%d&limit=%d&cursor=%s", scraper.id, PageSize, nextCursor)
		resp, err := client.Get(url)
		if err != nil {
			return nil, fmt.Errorf("Failed to get image list for user %s (page %d): %w\n", scraper.username, page, err)
		}

		var curPage imageList
		err = json.NewDecoder(resp.Body).Decode(&curPage)
		resp.Body.Close()

		nextCursor = curPage.Next_cursor

		if err != nil {
			return nil, fmt.Errorf("Failed to decode JSON imagelist response for user %s: %w\n", scraper.username, err)
		}

		for _, item := range curPage.Media {
			list = append(list, item.Media)
		}

		// No more new pages
		if nextCursor == "" {
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
		return "", fmt.Errorf("Failed to parse image URL for media %s: %w\n", media.Responsive_url, err)
	}

	// Trim to unix seconds
	uploadDate := strconv.Itoa(media.Upload_date)[:10]

	fileExt := path.Ext(parsed.Path)
	return fmt.Sprintf("%s%s", uploadDate, fileExt), nil
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
		return fmt.Errorf("Failed to download image %s: %w\n", mediaUrl, err)
	}

	// We care about the modification time
	imageTime := time.Unix(int64(media.Upload_date)/int64(1000), 0)
	os.Chtimes(imagePath, imageTime, imageTime)

	return nil
}

func stripExistingMedia(mediaList []Media, userPath string) ([]Media, error) {
	var strippedList []Media

	for _, media := range mediaList {
		mediaFilename, err := getMediaFilename(media)

		if err != nil {
			return nil, err
		}

		if _, exists := os.Stat(path.Join(userPath, mediaFilename)); exists != nil {
			strippedList = append(strippedList, media)
		}
	}

	return strippedList, nil
}

func createUserDirectory(username string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("Could not get cwd: %w\n", err)
	}

	userPath := path.Join(cwd, username)

	err = os.MkdirAll(userPath, 0755)

	if err != nil {
		return "", fmt.Errorf("Could not create directory %s: %w\n", userPath, err)
	}

	return userPath, nil
}

func (scraper *Scraper) SaveAllMedia() error {
	mediaList, err := scraper.fetchImageList()
	if err != nil {
		return err
	}

	userPath, err := createUserDirectory(scraper.username)
	if err != nil {
		return err
	}

	// Strip our list so we don't save duplicates
	mediaList, err = stripExistingMedia(mediaList, userPath)
	if err != nil {
		return err
	}

	// Dumb concurrency
	var sem = make(chan int, scraper.numWorkers)
	var wg sync.WaitGroup

	bar := progressbar.Default(int64(len(mediaList)), fmt.Sprintf("Downloading images from %s...", scraper.username))
	for _, media := range mediaList {
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
				log.Print(err)
			}
		}(media)
	}

	wg.Wait()

	return nil
}

func GetMediaFromUserlist(list string, numWorkers int, saveProfilePictures bool) error {
	file, err := os.Open(list)
	if err != nil {
		return fmt.Errorf("Failed to open file %s: %w\n", list, err)
	}

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		scraper := NewScraper(scanner.Text(), numWorkers)

		err := scraper.GetUserInfo()
		if err != nil {
			continue
		}

		// We don't stop for just one error
		if saveProfilePictures {
			err = scraper.SaveProfilePicture()
			if err != nil {
				log.Print(err)
			}
		} else {
			err = scraper.SaveAllMedia()
			if err != nil {
				log.Print(err)
			}
		}
	}

	return nil
}

func (scraper *Scraper) SaveProfilePicture() error {
	userPath, err := createUserDirectory(scraper.username)
	if err != nil {
		return err
	}

	profileFolder := path.Join(userPath, "profile")

	bar := progressbar.Default(1, fmt.Sprintf("Downloading profile picture of %s...", scraper.username))

	err = os.MkdirAll(profileFolder, 0755)
	if err != nil {
		return fmt.Errorf("Could not create directory %s: %w\n", profileFolder, err)
	}

	u, err := url.Parse(scraper.profileImage)
	if err != nil {
		return fmt.Errorf("Failed to parse profile image URL %s: %w\n", scraper.profileImage, err)
	}

	// Delete width and height params
	q := u.Query()
	q.Del("w")
	q.Del("h")

	u.RawQuery = q.Encode()
	fixedURL := u.String()

	err = client.DownloadFile(fixedURL, path.Join(profileFolder, fmt.Sprintf("%s.jpg", scraper.username)))
	if err != nil {
		return fmt.Errorf("Failed to download profile picture %s: %w\n", scraper.profileImage, err)
	}

	bar.Add(1)

	return nil
}
