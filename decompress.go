package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bitrise-io/go-utils/log"
	"github.com/pierrec/lz4/v4"
)

type DecompressReader struct {
	reader 					io.Reader
	closer					io.Closer
	decompressedFilePath	string
}

func NewDecompressReader(compressedFilePath, decompressAlgorithm string) (*DecompressReader, error) {
	var inputFile *os.File
	if compressedFilePath != "" {
		file, err := os.Open(compressedFilePath)
		if err != nil {
			return nil, fmt.Errorf("Fatal error in input compressed file: ", err.Error())
		}
		inputFile = file
	} else {
		inputFile = os.Stdin
	}

	fileInfo, err := inputFile.Stat()
	if err == nil {
		log.Infof("%s file found: %s, size: %d", compressedFilePath, fileInfo.Name(), fileInfo.Size())
	}

	decompressedFilePath := GetuncompressedFilePathFrom(compressedFilePath, decompressAlgorithm)

	if decompressAlgorithm == "lz4" {
		return &DecompressReader{
			reader: lz4.NewReader(inputFile),
			closer:	inputFile,
			decompressedFilePath: decompressedFilePath,
		}, nil
	}

	return nil, fmt.Errorf("Fatal error unsupported decompress algorithm")
}

func FastArchiveDecompress(compressedFilePath, decompressAlgorithm string) (*os.File) {
	log.Infof("Decompressing fast-archive using %s...", decompressAlgorithm)

	uncompressStartTime := time.Now()
	decompress, err := NewDecompressReader(compressedFilePath, decompressAlgorithm)
	if err != nil {
		failf("Fatal error in creating decompress reader: ", err.Error())

		return nil
	}

	defer decompress.closer.Close()

	out, err := os.Create(decompress.decompressedFilePath)
	if err != nil {
		failf("Fatal error in creating uncompressed file: ", err.Error())

		return nil
	}
	defer out.Close()

	_, err = io.Copy(out, decompress.reader)
	if err != nil {
		failf("Error decompressing file:", err.Error())

		return nil
	}

	err = os.Remove(compressedFilePath)
	if err != nil {
		failf("Error deleting compressed archive file: ", err.Error())

		return nil
	}

	decompressedFile, err := os.Open(decompress.decompressedFilePath)
	if err != nil {
		failf("Error opening uncompressed file: ", err.Error())

		return nil
	}

	log.Donef("Done uncompressing file in: ", time.Since(uncompressStartTime).String())

	return decompressedFile
}

func GetuncompressedFilePathFrom(path, decompressAlgorithm string) (string) {
	if decompressAlgorithm == "lz4" {
		return strings.ReplaceAll(path, ".lz4", "")
	} else if decompressAlgorithm == "gzip" {
		return strings.ReplaceAll(path, ".gz", "")
	}

	return path
}