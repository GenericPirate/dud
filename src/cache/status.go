package cache

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/kevin-hanselman/dud/src/artifact"
	"github.com/kevin-hanselman/dud/src/checksum"
	"github.com/kevin-hanselman/dud/src/fsutil"
	"github.com/pkg/errors"
)

// Status reports the status of an Artifact in the Cache.
func (ch LocalCache) Status(workspaceDir string, art artifact.Artifact) (
	outputStatus artifact.ArtifactWithStatus,
	err error,
) {
	outputStatus.Artifact = art
	if art.IsDir {
		outputStatus.Status, _, err = dirArtifactStatus(ch, workspaceDir, art)
	} else {
		outputStatus.Status, err = fileArtifactStatus(ch, workspaceDir, art)
	}
	err = errors.Wrapf(err, "status %s", art.Path)
	return
}

// quickStatus populates all artifact.Status fields except for ContentsMatch.
// However, this function will set ContentsMatch if the workspace file is
// a link and the other status booleans are true; checking to see if a link
// points to the cache is, as this function suggests, quick.
var quickStatus = func(
	// TODO: It may be worth exposing this version of status (bypassing the
	// full status check) using a CLI flag.
	ch LocalCache,
	workspaceDir string,
	art artifact.Artifact,
) (status artifact.Status, cachePath, workPath string, err error) {
	workPath = filepath.Join(workspaceDir, art.Path)
	cachePath, err = ch.PathForChecksum(art.Checksum)
	absCachePath := filepath.Join(ch.dir, cachePath)
	// TODO: assert InvalidChecksumError
	if err != nil { // An error means the checksum is invalid
		status.HasChecksum = false
	} else {
		status.HasChecksum = true
		// TODO: check for regular file?
		status.ChecksumInCache, err = fsutil.Exists(absCachePath, false)
		if err != nil {
			return
		}
	}
	status.WorkspaceFileStatus, err = fsutil.FileStatusFromPath(workPath)
	if err != nil {
		return
	}
	if status.HasChecksum &&
		status.ChecksumInCache &&
		status.WorkspaceFileStatus == fsutil.StatusLink {
		var linkDst string
		linkDst, err = os.Readlink(workPath)
		if err != nil {
			return
		}
		status.ContentsMatch = linkDst == absCachePath
	}
	return
}

func fileArtifactStatus(ch LocalCache, workspaceDir string, art artifact.Artifact) (artifact.Status, error) {
	status, cachePath, workPath, err := quickStatus(ch, workspaceDir, art)
	if err != nil {
		return status, err
	}
	cachePath = filepath.Join(ch.dir, cachePath)

	if status.WorkspaceFileStatus != fsutil.StatusRegularFile {
		return status, nil
	}

	if art.SkipCache {
		if !status.HasChecksum {
			return status, nil
		}
		fileReader, err := os.Open(workPath)
		if err != nil {
			return status, err
		}
		defer fileReader.Close()
		workspaceFileChecksum, err := checksum.Checksum(fileReader)
		if err != nil {
			return status, err
		}
		status.ContentsMatch = workspaceFileChecksum == art.Checksum
	} else {
		if !status.ChecksumInCache {
			return status, nil
		}
		status.ContentsMatch, err = fsutil.SameContents(workPath, cachePath)
		if err != nil {
			return status, err
		}
	}
	return status, nil
}

func dirArtifactStatus(
	ch LocalCache,
	workspaceDir string,
	art artifact.Artifact,
) (artifact.Status, directoryManifest, error) {
	var manifest directoryManifest
	status, cachePath, workPath, err := quickStatus(ch, workspaceDir, art)
	if err != nil {
		return status, manifest, err
	}
	cachePath = filepath.Join(ch.dir, cachePath)

	if !(status.HasChecksum && status.ChecksumInCache) {
		return status, manifest, nil
	}

	if status.WorkspaceFileStatus != fsutil.StatusDirectory {
		// TODO: Should this be an error?
		return status, manifest, nil
	}

	manifest, err = readDirManifest(cachePath)
	if err != nil {
		return status, manifest, err
	}

	// First, ensure all artifacts in the directoryManifest are up-to-date;
	// quit early if any are not.
	for _, art := range manifest.Contents {
		artStatus, err := ch.Status(workPath, *art)
		if err != nil {
			return status, manifest, err
		}
		if !artStatus.ContentsMatch {
			return status, manifest, nil
		}
	}

	// Second, get a directory listing and check for untracked files;
	// quit early if any exist.
	// TODO: Consider replacing ReadDir with filepath.Walk to better handle massive directories.
	entries, err := ioutil.ReadDir(workPath)
	if err != nil {
		return status, manifest, err
	}
	for _, entry := range entries {
		// Only check entries that don't appear in the manifest.
		if _, ok := manifest.Contents[entry.Name()]; !ok {
			if entry.IsDir() {
				// If the entry is a (untracked) directory, this is only
				// a mismatch if the artifact is recursive.
				if !art.DisableRecursion {
					return status, manifest, nil
				}
			} else {
				// If the entry is a (untracked) file, this is always
				// a mismatch.
				return status, manifest, nil
			}
		}
	}
	status.ContentsMatch = true
	return status, manifest, nil
}

func readDirManifest(path string) (man directoryManifest, err error) {
	manifestFile, err := os.Open(path)
	if err != nil {
		return
	}
	defer manifestFile.Close()
	err = json.NewDecoder(manifestFile).Decode(&man)
	return
}
