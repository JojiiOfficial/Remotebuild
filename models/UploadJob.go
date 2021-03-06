package models

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DataManager-Go/libdatamanager"
	libremotebuild "github.com/RemoteBuild/LibRemotebuild"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var (
	// ErrNoManagerDataAvailable if no datamanager data is available but required
	ErrNoManagerDataAvailable = errors.New("No DManager data available")

	// ErrNoVaildUploadMetodPassed if no uploadmethod/data was passed
	ErrNoVaildUploadMetodPassed = errors.New("No vaild upolad method passed")
)

// UploadJob a job which uploads a built package
type UploadJob struct {
	gorm.Model
	State libremotebuild.JobState // Upload state

	Type libremotebuild.UploadType

	cancelChan chan bool `gorm:"-"` // Cancel chan
}

// UploadJobResult result of uploading a binary
type UploadJobResult struct {
	Error error
}

// NewUploadJob create new upload job
func NewUploadJob(db *gorm.DB, uploadJob UploadJob) (*UploadJob, error) {
	uploadJob.State = libremotebuild.JobWaiting
	uploadJob.cancelChan = make(chan bool, 1)

	// Save Job into DB
	err := db.Create(&uploadJob).Error
	if err != nil {
		return nil, err
	}

	return &uploadJob, nil
}

// Init the uploadJob
func (uploadJob *UploadJob) Init() {
	if uploadJob.cancelChan == nil {
		uploadJob.cancelChan = make(chan bool, 1)
	}
}

// Run an upload job
func (uploadJob *UploadJob) Run(buildResult BuildResult, argParser *ArgParser, config *Config) *UploadJobResult {
	uploadJob.Init()

	log.Debug("Run UploadJob ", uploadJob.ID)
	uploadJob.State = libremotebuild.JobRunning

	// Verify Dmanager data
	if uploadJob.Type == libremotebuild.DataManagerUploadType && !argParser.HasDataManagerArgs() {
		return &UploadJobResult{
			Error: ErrNoManagerDataAvailable,
		}
	}

	return uploadJob.upload(buildResult, argParser, config)
}

func (uploadJob *UploadJob) upload(buildResult BuildResult, argParser *ArgParser, config *Config) *UploadJobResult {
	// Pick correct upload method
	switch uploadJob.Type {
	case libremotebuild.DataManagerUploadType:
		return uploadJob.uploadDmanager(buildResult, argParser)
	case libremotebuild.LocalStorage:
		return uploadJob.saveToLocalStorage(buildResult, argParser, config)
	}

	// If no uploadtype was set, return error
	uploadJob.State = libremotebuild.JobFailed
	return &UploadJobResult{
		Error: ErrNoVaildUploadMetodPassed,
	}
}

// Save output to the local storage directory
func (uploadJob *UploadJob) saveToLocalStorage(buildResult BuildResult, argParser *ArgParser, config *Config) *UploadJobResult {
	log.Info("save to local store")

	path := filepath.Clean(filepath.Join(config.Server.LocalStoragePath, fmt.Sprintf("%d-%s-%s", buildResult.resinfo.JobID, buildResult.resinfo.Name, buildResult.resinfo.Version)))

	// should not happen
	if _, err := os.Stat(path); err == nil {
		// Path already exists
		log.Debug("Clearing old build result")
		err = os.RemoveAll(path)
		if err != nil {
			return &UploadJobResult{
				Error: err,
			}
		}
	}

	// Copy all files to the LocalStoragePath
	for _, file := range buildResult.resinfo.Files {
		err := Copy(file, path)
		if err != nil {
			return &UploadJobResult{
				Error: err,
			}
		}
	}

	uploadJob.State = libremotebuild.JobDone
	return nil
}

// Upload to datamanager
// See https://github.com/DataManager-Go/DataManagerServer
func (uploadJob *UploadJob) uploadDmanager(buildResult BuildResult, argParser *ArgParser) *UploadJobResult {
	dmanagerData := argParser.GetDManagerData()

	// Decode base64 encoded token
	unencodedToken, err := base64Decode(dmanagerData.Token)
	if err != nil {
		uploadJob.State = libremotebuild.JobFailed
		return &UploadJobResult{
			Error: err,
		}
	}

	attributes := libdatamanager.FileAttributes{
		Groups: []string{buildResult.resinfo.Name},
		Tags:   []string{buildResult.resinfo.Version},
	}

	// Set namespace if provided
	if argParser.HasNamespace() {
		attributes.Namespace = dmanagerData.Namespace
	} else {
		attributes.Groups = append(attributes.Groups, "AURPackage")
	}

	for _, file := range buildResult.resinfo.Files {
		// Create uploadrequest
		uploadRequest := libdatamanager.NewLibDM(&libdatamanager.RequestConfig{
			URL:          dmanagerData.Host,
			Username:     dmanagerData.Username,
			SessionToken: unencodedToken,
		}).NewUploadRequest(filepath.Base(file), attributes)

		f, err := os.Open(file)
		if err != nil {
			uploadJob.State = libremotebuild.JobFailed
			return &UploadJobResult{
				Error: err,
			}
		}

		// Upload file
		_, err = uploadRequest.UploadFile(f, nil, uploadJob.cancelChan)
		if err != nil {
			uploadJob.State = libremotebuild.JobFailed
			return &UploadJobResult{
				Error: err,
			}
		}
	}

	uploadJob.State = libremotebuild.JobDone
	return nil
}

// Cancel a buildJob
func (uploadJob *UploadJob) cancel() {
	if uploadJob.State == libremotebuild.JobRunning {
		uploadJob.cancelChan <- true
	}

	uploadJob.State = libremotebuild.JobCancelled
}
