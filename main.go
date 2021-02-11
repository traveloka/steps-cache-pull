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
	CacheAPIURL      	string `env:"cache_api_url"`
	DebugMode        	bool   `env:"is_debug_mode,opt[true,false]"`
	StackID          	string `env:"BITRISEIO_STACK_ID"`
	BuildSlug        	string `env:"BITRISE_BUILD_SLUG"`
	UseFastArchive   	string `env:"use_fast_archive,opt[true,false]"`
	DecompressArchive  	string `env:"decompress_algorithm,opt[none,lz4,gzip,zstd,pgzip]"`
	CacheAppSlug      	string `env:"cache_app_slug"`
	AccessToken      	string `env:"access_token"`
}

func (l *MultiLevelLogger) Verbose(v ...interface{}) {
	if l.verbose {
		l.logger.Println(v...)
	}
}
func (l *MultiLevelLogger) Warning(v ...interface{}) {
	l.logger.Println(v...)
}

// HTTP header
func defaultHeader(conf Config) map[string][]string {
    return map[string][]string{
        "Content-Type": {"application/json"},
        "Authorization": {conf.AccessToken},
    }
}

// Get latest build slug from default branch in specified app slug
func getLatestBuildDefaultBranchArtifact(conf Config) string {
    branch, err := defaultGitBranch()
    if err != nil {
        failf("failed to get default branch")
    }

    log.Infof("got default branch: " + branch)

    latestBuildSlug, nextCursor := "", ""
    client := &http.Client{}

    for latestBuildSlug == "" {
        url := ""
        if nextCursor == "" {
            url = "https://api.bitrise.io/v0.1/apps/" + conf.CacheAppSlug + "/builds?status=1"
        } else {
            url = "https://api.bitrise.io/v0.1/apps/" + conf.CacheAppSlug + "/builds?status=1&next=" + nextCursor
        }

        req, _ := http.NewRequest("GET", url, nil)

        req.Header = defaultHeader(conf)
        res, err := client.Do(req)

        defer res.Body.Close()

        if err != nil {
            failf("The HTTP request failed with error %s\n", err)
        }

        data, _ := ioutil.ReadAll(res.Body)

        jsonMap := make(map[string]interface{})
        err = json.Unmarshal([]byte(string(data)), &jsonMap)
        if err != nil {
            failf("Fail unmarshal json data with error %s\n", err)
        }

        jsonData, _ := jsonMap["data"].([]interface{})
        if jsonData != nil {
            for _, buildJson := range jsonData {
                if jsonBuild, ok := buildJson.(map[string]interface{}); ok && jsonBuild["branch"].(string) == branch {
                    log.Infof("Found latest build: %s with slug: %s", jsonBuild["branch"], jsonBuild["slug"])

                    latestBuildSlug = jsonBuild["slug"].(string)
                    break
                }
            }
        }

        paging, _ := jsonMap["paging"].(map[string]interface{})

        if paging["next"] != nil {
            nextCursor = paging["next"].(string)
        }
    }

    return latestBuildSlug
}

// Download artifact from specified app slug and build slug
func downloadArtifact(conf Config, build_slug string) (string, error) {
    downloadStartTime := time.Now()
	fmt.Println()
	log.Infof("Downloading artifact cache from: %s build: %s", conf.CacheAppSlug, build_slug)

    url := "https://api.bitrise.io/v0.1/apps/" + conf.CacheAppSlug + "/builds/" + build_slug + "/artifacts"
    req, _ := http.NewRequest("GET", url, nil)

    req.Header = defaultHeader(conf)

    client := &http.Client{}
    res, err := client.Do(req)

    defer res.Body.Close()

    if err != nil {
        failf("The HTTP request failed with error %s\n", err)
    }

    data, _ := ioutil.ReadAll(res.Body)

    jsonMap := make(map[string]interface{})
    err = json.Unmarshal([]byte(string(data)), &jsonMap)
    if err != nil {
        failf("Fail unmarshal json data with error %s\n", err)
    }

    jsonData, _ := jsonMap["data"].([]interface{})
    foundArtifact := false
    if jsonData != nil {
        for _, buildJson := range jsonData {
            if jsonArtifact, ok := buildJson.(map[string]interface{}); ok && strings.Contains(jsonArtifact["title"].(string), "buck-cache") {
                foundArtifact = true
                log.Infof("Found artifacts: %s", jsonArtifact["title"])

                // Download the artifact
                url := "https://api.bitrise.io/v0.1/apps/" + conf.CacheAppSlug + "/builds/" + build_slug + "/artifacts/" + jsonArtifact["slug"].(string)
                req, _ := http.NewRequest("GET", url, nil)
                req.Header = defaultHeader(conf)

                client := &http.Client{}
                resp, err := client.Do(req)
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

            	cachePath := "/tmp/" + jsonArtifact["title"].(string)

                f, err := os.Create(cachePath)
            	if err != nil {
            		return "", fmt.Errorf("failed to open the local cache file for write: %s", err)
            	}

            	var bytesWritten int64
            	bytesWritten, err = io.Copy(f, resp.Body)
            	if err != nil {
            		return "", err
            	}

                log.Infof("Download %s success. Size %d", jsonArtifact["title"], bytesWritten)
            }
        }
    }

    if !foundArtifact {
        return "", fmt.Errorf("Artifact not found. You may haven't build or had cache before.")
    }

    fmt.Println()
    log.Donef("Done downloading cache in: ", time.Since(downloadStartTime).String())

    cacheArchivePath := "/tmp/cache-archive.tar"
    if conf.UseFastArchive == "true" {
		cacheArchivePath = "/tmp/cache-archive.fast-archive"
	}

	if err := MergeCache(cacheArchivePath); err != nil {
	    return "", fmt.Errorf("failed when merging split cache: %s", err)
	}

    return cacheArchivePath, nil
}

// downloadCacheArchive downloads the cache archive and returns the downloaded file's path.
// If the URI points to a local file it returns the local paths.
func downloadCacheArchive(url string, conf Config) (string, error) {
	if strings.HasPrefix(url, "file://") {
		uri := strings.TrimPrefix(url, "file://")
		if conf.DecompressArchive != "none" {
			uri = GetCompressedFilePathFrom(uri, conf.DecompressArchive)
		}

		return uri, nil
	}

	downloadStartTime := time.Now()
	fmt.Println()
	log.Infof("Downloading remote cache archive")

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
    if conf.UseFastArchive == "true" {
		cacheArchivePath = "/tmp/cache-archive.fast-archive"	
	}

	if conf.DecompressArchive != "none" {
		cacheArchivePath = GetCompressedFilePathFrom(cacheArchivePath, conf.DecompressArchive)
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
		"build_slug":         conf.BuildSlug,
	}
	log.Debugf("Size of downloaded cache archive: %d Bytes", bytesWritten)
	log.RInfof(stepID, "cache_fallback_archive_size", data, "Size of downloaded cache archive: %d Bytes", bytesWritten)

	fmt.Println()
    log.Donef("Done downloading cache in: ", time.Since(downloadStartTime).String())

	return cacheArchivePath, nil
}

// performRequest performs an http request and returns the response's body, if the status code is 200.
func performRequest(url string) (io.ReadCloser, error) {
	fmt.Println()
	log.Infof("Downloading remote cache archive")

	downloadStartTime := time.Now()
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

	fmt.Println()
    log.Donef("Done downloading cache in: ", time.Since(downloadStartTime).String())

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

	log.Infof("Cache URL: %s", conf.CacheAPIURL)

	if conf.UseFastArchive == "true" {
	    // Use Fast Archive
	    var pth string
	    var errDownload error
	    if conf.CacheAppSlug != "" {
	        buildSlug := getLatestBuildDefaultBranchArtifact(conf)
	        pth, errDownload = downloadArtifact(conf, buildSlug)

	    } else {
            pth, errDownload = downloadCacheArchive(cacheURI, conf)
	    }

	    if errDownload != nil {
	        failf("Error downloading archive: %s", errDownload)
	    }


        var inputFile *os.File
		if conf.DecompressArchive != "none" {
			inputFile = FastArchiveDecompress(pth, conf.DecompressArchive)
		} else {
			if pth != "" {
				file, err := os.Open(pth)
				if err != nil {
					failf("Error opening input file:", err.Error())
				}
				inputFile = file
			} else {
				inputFile = os.Stdin
			}
		}

		defer inputFile.Close()

		fileInfo, err := inputFile.Stat()
		if err == nil {
			log.Infof("Fast archive file found: %s, size: %d", fileInfo.Name(), fileInfo.Size())
		}

		fmt.Println()
        log.Infof("Extracting cache archive using fast archive...")

		unarchiveTime := time.Now()
        unarchiver := falib.NewUnarchiver(inputFile)
		unarchiver.Logger = &MultiLevelLogger{syslog.New(os.Stderr, "", 0), false}
		unarchiver.IgnorePerms = false
		unarchiver.IgnoreOwners = false
		unarchiver.DryRun = false
		err = unarchiver.Run()
        if err != nil {
        	failf("Fatal error in archiver:", err.Error())
		}
		fmt.Println()
    	log.Donef("Done unarchiving fast-archive in: ", time.Since(unarchiveTime).String())
	} else {
	    // Use Tar Archive
		tarUnarchiveStartTime := time.Now()
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

			pth, err := downloadCacheArchive(cacheURI, conf)
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

		fmt.Println()
    	log.Donef("Done unarchiving tar in: ", time.Since(tarUnarchiveStartTime).String())
	}

	fmt.Println()
	log.Donef("Done")
	log.Printf("Total step time: " + time.Since(startTime).String())
}
