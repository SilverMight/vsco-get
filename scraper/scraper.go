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

type mediaList struct {
	Media       []mediaWrapper `json:"media"`
	Total       int            `json:"total"`
	Next_cursor string         `json:"next_cursor"`
}

type mediaWrapper struct {
	Type  string    `json:"type"`
	Media mediaItem `json:"-"`
}

// mediaItem interface for handling different media types
type mediaItem interface {
	GetURL() string
	GetUploadDate() int64
	GetFilename() (string, error)
	IsVideo() bool
	Save(folderPath string) error
}

// Image content or old-style videos
type imageMedia struct {
	Is_video       bool   `json:"is_video"`
	Video_url      string `json:"video_url"`
	Responsive_url string `json:"responsive_url"`
	Upload_date    int    `json:"upload_date"`
}

func (i imageMedia) GetURL() string {
	if i.Is_video {
		return i.Video_url
	}

	return i.Responsive_url
}

func (i imageMedia) GetUploadDate() int64 {
	return int64(i.Upload_date)
}

func (i imageMedia) IsVideo() bool {
	return i.Is_video
}

func (i imageMedia) GetFilename() (string, error) {
	mediaUrl := i.GetURL()

	parsed, err := url.Parse(mediaUrl)
	if err != nil {
		return "", fmt.Errorf("Failed to parse image URL %s: %w", mediaUrl, err)
	}

	// Trim to unix seconds
	uploadDate := strconv.FormatInt(i.GetUploadDate(), 10)
	const trimLength = 10
	if len(uploadDate) > trimLength {
		uploadDate = uploadDate[:trimLength]
	}

	fileExt := path.Ext(parsed.Path)

	return fmt.Sprintf("%s%s", uploadDate, fileExt), nil
}

func (i imageMedia) Save(folderPath string) error {
	return saveMediaToFile(i, folderPath)
}

// New style videos, typically stored in m3u8 playlists
// NOTE: saving is unimplemented currently for this
type videoMedia struct {
	Playback_url string `json:"playback_url"`
	Created_date int64  `json:"created_date"`
	Has_audio    bool   `json:"has_audio"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

func (v videoMedia) GetURL() string {
	return v.Playback_url
}

func (v videoMedia) GetUploadDate() int64 {
	return v.Created_date / 1000
}

func (v videoMedia) IsVideo() bool {
	return true
}

func (v videoMedia) GetFilename() (string, error) {
	uploadDate := strconv.FormatInt(v.GetUploadDate(), 10)
	const trimLength = 10
	if len(uploadDate) > trimLength {
		uploadDate = uploadDate[:trimLength]
	}

	return fmt.Sprintf("%s.mp4", uploadDate), nil
}

func (v videoMedia) Save(folderPath string) error {
	// TODO: Implement video saving with m3u8 playlists, may need to
	// use ffmpeg...
	log.Printf("Video media downloading new yet implemented, URL is %s\n", v.GetURL())
	return nil
}

func (mw *mediaWrapper) UnmarshalJSON(data []byte) error {
	var rawJson map[string]json.RawMessage

	if err := json.Unmarshal(data, &rawJson); err != nil {
		return err
	}

	if typeBytes, exists := rawJson["type"]; exists {
		if err := json.Unmarshal(typeBytes, &mw.Type); err != nil {
			return err
		}
	}

	var mediaBytes json.RawMessage
	var exists bool

	// Handle different media types
	switch mw.Type {
	case "image":
		mediaBytes, exists = rawJson["image"]
		if exists {
			var imageMedia imageMedia
			if err := json.Unmarshal(mediaBytes, &imageMedia); err != nil {
				return fmt.Errorf("Failed to unmarshal image: %w", err)
			}
			mw.Media = imageMedia
		}
	case "video":
		mediaBytes, exists = rawJson["video"]
		if exists {
			var videoMedia videoMedia
			if err := json.Unmarshal(mediaBytes, &videoMedia); err != nil {
				return fmt.Errorf("Failed to unmarshal video: %w", err)
			}
			mw.Media = videoMedia
		}
	default:
		fmt.Printf("Unknown media type %s detected, skipping...\n", mw.Type)
		return nil
	}

	if !exists {
		return fmt.Errorf("Missing key matching media type: %s", mw.Type)
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
	// The actual webpage seems to use 14 here, but the upper bound seems
	// to actually be 30 - use this to keep num of requests down
	PageSize = 30
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

func (scraper *Scraper) fetchMediaList() ([]mediaItem, error) {
	var list []mediaItem

	nextCursor := ""
	for page := 1; ; page++ {
		url := fmt.Sprintf("https://vsco.co/api/3.0/medias/profile?site_id=%d&limit=%d&cursor=%s", scraper.id, PageSize, url.QueryEscape(nextCursor))
		resp, err := client.Get(url)
		if err != nil {
			return nil, fmt.Errorf("Failed to get media list for user %s (page %d): %w\n", scraper.username, page, err)
		}

		var curPage mediaList

		err = json.NewDecoder(resp.Body).Decode(&curPage)
		resp.Body.Close()

		nextCursor = curPage.Next_cursor

		if err != nil {
			return nil, fmt.Errorf("Failed to decode JSON media list response for user %s: %w\n", scraper.username, err)
		}

		for _, item := range curPage.Media {
			if item.Media != nil {
				list = append(list, item.Media)
			}
		}

		// No more new pages
		if nextCursor == "" {
			break
		}
	}

	return list, nil
}

func fixUrl(rawUrl string) (fixedUrl string) {
	if strings.HasPrefix(rawUrl, "https://") {
		return rawUrl
	}
	return "https://" + rawUrl
}

func saveMediaToFile(media mediaItem, folderPath string) error {
	mediaUrl := media.GetURL()
	mediaUrl = fixUrl(mediaUrl)

	mediaFile, err := media.GetFilename()
	if err != nil {
		return err
	}

	mediaPath := path.Join(folderPath, mediaFile)

	err = client.DownloadFile(mediaUrl, mediaPath)
	if err != nil {
		return fmt.Errorf("Failed to download media %s: %w\n", mediaUrl, err)
	}

	// We care about the modification time
	var mediaTime time.Time
	uploadDate := media.GetUploadDate()
	if uploadDate != 0 {
		mediaTime = time.Unix(uploadDate/1000, 0)
	} else {
		mediaTime = time.Now()
	}

	os.Chtimes(mediaPath, mediaTime, mediaTime)

	return nil
}

func stripExistingMedia(mediaList []mediaItem, userPath string) ([]mediaItem, error) {
	var strippedList []mediaItem

	for _, media := range mediaList {
		mediaFilename, err := media.GetFilename()
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
	mediaList, err := scraper.fetchMediaList()
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

	bar := progressbar.Default(int64(len(mediaList)), fmt.Sprintf("Downloading media from %s...", scraper.username))
	for _, media := range mediaList {
		sem <- 1
		wg.Add(1)
		go func(media mediaItem) {
			defer func() {
				<-sem
				wg.Done()
				bar.Add(1)
			}()

			err := media.Save(userPath)
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
