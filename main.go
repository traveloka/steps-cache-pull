package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	syslog "log"
	"time"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/log"
	"github.com/hendych/fast-archiver/falib"
)

const (
	stepID = "cache-pull"
)

type MultiLevelLogger struct {
	logger  *syslog.Logger
	verbose bool
}

// Config stores the step inputs.
type Config struct {
	CacheAPIURL      string `env:"cache_api_url"`
	DebugMode        bool   `env:"is_debug_mode,opt[true,false]"`
	StackID          string `env:"BITRISEIO_STACK_ID"`
	BuildSlug        string `env:"BITRISE_BUILD_SLUG"`
	UseFastArchive   string `env:"use_fast_archive,opt[true,false]"`
}

func (l *MultiLevelLogger) Verbose(v ...interface{}) {
	if l.verbose {
		l.logger.Println(v...)
	}
}
func (l *MultiLevelLogger) Warning(v ...interface{}) {
	l.logger.Println(v...)
}

// downloadCacheArchive downloads the cache archive and returns the downloaded file's path.
// If the URI points to a local file it returns the local paths.
func downloadCacheArchive(url string, buildSlug string, use_fast_archive bool) (string, error) {
	if strings.HasPrefix(url, "file://") {
		return strings.TrimPrefix(url, "file://"), nil
	}

	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnf("Failed to close response body: %s", err)
		}
	}()

	if resp.StatusCode != 200 {
		responseBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		return "", fmt.Errorf("non success response code: %d, body: %s", resp.StatusCode, string(responseBytes))
	}

    cacheArchivePath := "/tmp/cache-archive.tar"
    if use_fast_archive {
        cacheArchivePath = "/tmp/cache-archive.fast-archive"
    }

	f, err := os.Create(cacheArchivePath)
	if err != nil {
		return "", fmt.Errorf("failed to open the local cache file for write: %s", err)
	}

	var bytesWritten int64
	bytesWritten, err = io.Copy(f, resp.Body)
	if err != nil {
		return "", err
	}

	data := map[string]interface{}{
		"cache_archive_size": bytesWritten,
		"build_slug":         buildSlug,
	}
	log.Debugf("Size of downloaded cache archive: %d Bytes", bytesWritten)
	log.RInfof(stepID, "cache_fallback_archive_size", data, "Size of downloaded cache archive: %d Bytes", bytesWritten)

	return cacheArchivePath, nil
}

// performRequest performs an http request and returns the response's body, if the status code is 200.
func performRequest(url string) (io.ReadCloser, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Warnf("Failed to close response body: %s", err)
			}
		}()

		responseBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		return nil, fmt.Errorf("non success response code: %d, body: %s", resp.StatusCode, string(responseBytes))
	}

	return resp.Body, nil
}

// getCacheDownloadURL gets the given build's cache download URL.
func getCacheDownloadURL(cacheAPIURL string) (string, error) {
	req, err := http.NewRequest("GET", cacheAPIURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %s", err)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %s", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnf("Failed to close response body: %s", err)
		}
	}()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("request sent, but failed to read response body (http-code: %d): %s", resp.StatusCode, body)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 202 {
		return "", fmt.Errorf("build cache not found: probably cache not initialised yet (first cache push initialises the cache), nothing to worry about ;)")
	}

	var respModel struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(body, &respModel); err != nil {
		return "", fmt.Errorf("failed to parse JSON response (%s): %s", body, err)
	}

	if respModel.DownloadURL == "" {
		return "", errors.New("download URL not included in the response")
	}

	return respModel.DownloadURL, nil
}

// parseStackID reads the stack id from the given json bytes.
func parseStackID(b []byte) (string, error) {
	type ArchiveInfo struct {
		StackID string `json:"stack_id,omitempty"`
	}
	var archiveInfo ArchiveInfo
	if err := json.Unmarshal(b, &archiveInfo); err != nil {
		return "", err
	}
	return archiveInfo.StackID, nil
}

// failf prints an error and terminates the step.
func failf(format string, args ...interface{}) {
	log.Errorf(format, args...)
	os.Exit(1)
}

func main() {
	var conf Config
	if err := stepconf.Parse(&conf); err != nil {
		failf(err.Error())
	}
	stepconf.Print(conf)
	log.SetEnableDebugLog(conf.DebugMode)

	if conf.CacheAPIURL == "" {
		log.Warnf("No Cache API URL specified, there's no cache to use, exiting.")
		return
	}

	startTime := time.Now()

	var cacheReader io.Reader
	var cacheURI string

	if strings.HasPrefix(conf.CacheAPIURL, "file://") {
		cacheURI = conf.CacheAPIURL

		fmt.Println()
		log.Infof("Using local cache archive")

		pth := strings.TrimPrefix(conf.CacheAPIURL, "file://")

        if conf.UseFastArchive == "false" {
    		var err error
    		cacheReader, err = os.Open(pth)
    		if err != nil {
    			failf("Failed to open cache archive file: %s", err)
    		}
        }
	} else {
		fmt.Println()
		log.Infof("Downloading remote cache archive")

		downloadURL, err := getCacheDownloadURL(conf.CacheAPIURL)
		if err != nil {
			failf("Failed to get cache download url: %s", err)
		}
		cacheURI = downloadURL

        if conf.UseFastArchive == "false" {
    		cacheReader, err = performRequest(downloadURL)
    		if err != nil {
    			failf("Failed to perform cache download request: %s", err)
    		}
        }
	}

	if conf.UseFastArchive == "true" {
	    // Use Fast Archive

	    fmt.Println()
        log.Infof("Downloading cache fast archive...")

    	pth, err := downloadCacheArchive(cacheURI, conf.BuildSlug, conf.UseFastArchive == "true")
    	if err != nil {
    		failf("Unable to download cache fast archive: %s", err)
    	}

	    fmt.Println()
        log.Infof("Extracting cache archive using fast archive on: ", cacheReader)

        var inputFile *os.File
		if pth != "" {
			file, err := os.Open(pth)
			if err != nil {
				failf("Error opening input file:", err.Error())
			}
			inputFile = file
		} else {
			inputFile = os.Stdin
		}

        unarchiver := falib.NewUnarchiver(inputFile)
		unarchiver.Logger = &MultiLevelLogger{syslog.New(os.Stderr, "", 0), false}
		unarchiver.IgnorePerms = false
		unarchiver.IgnoreOwners = false
		unarchiver.DryRun = false
		err = unarchiver.Run()
        if err != nil {
        	failf("Fatal error in archiver:", err.Error())
        }
        inputFile.Close()
	} else {
	    // Use Tar Archive

	    cacheRecorderReader := NewRestoreReader(cacheReader)
    	currentStackID := strings.TrimSpace(conf.StackID)
    	if len(currentStackID) > 0 {
    		fmt.Println()
    		log.Infof("Checking archive and current stacks")
    		log.Printf("current stack id: %s", currentStackID)

    		r, hdr, err := readFirstEntry(cacheRecorderReader)
    		if err != nil {
    			failf("Failed to get first archive entry: %s", err)
    		}

    		cacheRecorderReader.Restore()

    		if filepath.Base(hdr.Name) == "archive_info.json" {
    			b, err := ioutil.ReadAll(r)
    			if err != nil {
    				failf("Failed to read first archive entry: %s", err)
    			}

    			archiveStackID, err := parseStackID(b)
    			if err != nil {
    				failf("Failed to parse first archive entry: %s", err)
    			}
    			log.Printf("archive stack id: %s", archiveStackID)

    			if archiveStackID != currentStackID {
    				log.Warnf("Cache was created on stack: %s, current stack: %s", archiveStackID, currentStackID)
    				log.Warnf("Skipping cache pull, because of the stack has changed")
    				os.Exit(0)
    			}
    		} else {
    			log.Warnf("cache archive does not contain stack information, skipping stack check")
    		}
    	}

    	fmt.Println()
    	log.Infof("Extracting cache archive")

    	if err := extractCacheArchive(cacheRecorderReader); err != nil {
    		log.Warnf("Failed to uncompress cache archive stream: %s", err)
    		log.Warnf("Downloading the archive file and trying to uncompress using tar tool")
    		data := map[string]interface{}{
    			"archive_bytes_read": cacheRecorderReader.BytesRead,
    			"build_slug":         conf.BuildSlug,
    		}
    		log.RInfof(stepID, "cache_archive_fallback", data, "Failed to uncompress cache archive stream: %s", err)

    		pth, err := downloadCacheArchive(cacheURI, conf.BuildSlug, conf.UseFastArchive == "true")
    		if err != nil {
    			failf("Fallback failed, unable to download cache archive: %s", err)
    		}

    		if err := uncompressArchive(pth); err != nil {
    			failf("Fallback failed, unable to uncompress cache archive file: %s", err)
    		}
    	} else {
    		data := map[string]interface{}{
    			"cache_archive_size": cacheRecorderReader.BytesRead,
    			"build_slug":         conf.BuildSlug,
    		}
    		log.Debugf("Size of extracted cache archive: %d Bytes", cacheRecorderReader.BytesRead)
    		log.RInfof(stepID, "cache_archive_size", data, "Size of extracted cache archive: %d Bytes", cacheRecorderReader.BytesRead)
    	}
	}

	fmt.Println()
	log.Donef("Done")
	log.Printf("Took: " + time.Since(startTime).String())
}
