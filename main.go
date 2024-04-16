package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	vsco "github.com/SilverMight/vsco-get/scraper"
)

func main() {
	usernameList := flag.String("l", "", "Scrape from text file containing a list of usernames for batch scraping (one per line).")
	numWorkers := flag.Int("w", 30, "Number of concurrent workers to download images.")
	getProfilePicture := flag.Bool("p", false, "Get profile pictures of a user.")

	flag.Parse()
	args := flag.Args()

	if len(args) > 0 {
		scraper := vsco.NewScraper(args[0], *numWorkers)
		err := scraper.GetUserInfo()
		if err != nil {
			log.Fatal(err)
		}

		if *getProfilePicture {
			err := scraper.SaveProfilePicture()
			if err != nil {
				log.Fatal(err)
			}
		} else {
			err = scraper.SaveAllMedia()
			if err != nil {
				log.Fatal(err)
			}
		}
	} else if *usernameList != "" {
		err := vsco.GetMediaFromUserlist(*usernameList, *numWorkers, *getProfilePicture)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		fmt.Printf("Usage: %s [flags] username\n", os.Args[0])
		flag.PrintDefaults()
		return
	}
}
