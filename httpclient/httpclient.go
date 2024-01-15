package httpclient

import (
	"io"
	"net/http"
	"os"
	"time"
)

type HttpClient struct {
	client http.Client
}

const (
	timeout            = time.Second * 10
	authorizationToken = "Bearer 7356455548d0a1d886db010883388d08be84d0c9"
	userAgent          = "vsco-get"
)

func NewClient() *HttpClient {
	return &HttpClient{http.Client{Timeout: timeout}}
}

func (client *HttpClient) Get(url string) (resp *http.Response, err error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	return client.do(req)
}

func (client *HttpClient) do(req *http.Request) (*http.Response, error) {
	req.Header.Add("Authorization", authorizationToken)
	req.Header.Add("User-Agent", userAgent)

	return client.client.Do(req)
}

func (client *HttpClient) DownloadFile(url string, file string) (err error) {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(file)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}
