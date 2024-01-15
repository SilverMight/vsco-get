# vsco-get

vsco-get is a simple and fast command-line tool written in Go that lets you scrape images from VSCO profiles.

# Features
* Download images from VSCO profiles.
* Scrape from a list of multiple profiles.
* Concurrent downloading for high performance.
* Configurable number of worker processes (be careful and respectful doing this).

## Installation

### Binary Releases

You can download and install a binary for your system from the **[releases page](https://github.com/SilverMight/vsco-get/releases)**.

### Build from Source

Clone or download the respository.
```
git clone https://github.com/SilverMight/vsco-get.git
```

Change into the directory and build the binary.
```
go build -o vsco-get main.go
```

Run the binary with `./vsco-get`.

## Usage

### Single User Scraping

./vsco-get username

### Multi User Scraping

./vsco-get -l usernames.txt


Replace "vsco-get" with the name of your binary, and "userlist.txt" with a text file containing a list of VSCO usernames, one per line.

## Options

- "-l": Specify a text file containing a list of usernames for batch scraping.
- "-w": Specify number of worker processes.


## License

This project is licensed under the **[GPL license](https://github.com/SilverMight/vsco-get/blob/main/LICENSE)**.
