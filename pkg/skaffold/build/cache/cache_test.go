/*
Copyright 2019 The Skaffold Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cache

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/build"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/config"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/docker"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/latest"
	"github.com/GoogleContainerTools/skaffold/testutil"
	"github.com/docker/docker/api/types"
	yaml "gopkg.in/yaml.v2"
)

var (
	digest    = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	digestOne = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	image     = fmt.Sprintf("image@%s", digest)
	imageOne  = fmt.Sprintf("image1@%s", digestOne)
)

var defaultArtifactCache = ArtifactCache{"hash": ImageDetails{
	Digest: "digest",
	ID:     "id",
}}

func mockHashForArtifact(hashes map[string]string) func(context.Context, build.Builder, *latest.Artifact) (string, error) {
	return func(ctx context.Context, _ build.Builder, a *latest.Artifact) (string, error) {
		return hashes[a.ImageName], nil
	}
}

func Test_NewCache(t *testing.T) {
	tests := []struct {
		updateCacheFile   bool
		needsPush         bool
		updateClient      bool
		name              string
		opts              *config.SkaffoldOptions
		expectedCache     *Cache
		api               *testutil.FakeAPIClient
		cacheFileContents interface{}
	}{
		{
			name:              "get a valid cache from file",
			cacheFileContents: defaultArtifactCache,
			updateCacheFile:   true,
			opts: &config.SkaffoldOptions{
				CacheArtifacts: true,
			},
			updateClient: true,
			api: &testutil.FakeAPIClient{
				ImageSummaries: []types.ImageSummary{
					{
						ID: "image",
					},
				},
			},
			expectedCache: &Cache{
				artifactCache: defaultArtifactCache,
				useCache:      true,
				imageList: []types.ImageSummary{
					{
						ID: "image",
					},
				},
			},
		},
		{
			name:              "needs push",
			cacheFileContents: defaultArtifactCache,
			needsPush:         true,
			updateCacheFile:   true,
			updateClient:      true,
			opts: &config.SkaffoldOptions{
				CacheArtifacts: true,
			},
			api: &testutil.FakeAPIClient{},
			expectedCache: &Cache{
				artifactCache: defaultArtifactCache,
				useCache:      true,
				needsPush:     true,
			},
		},
		{
			name:              "valid cache file exists, but useCache is false",
			cacheFileContents: defaultArtifactCache,
			api:               &testutil.FakeAPIClient{},
			opts:              &config.SkaffoldOptions{},
			expectedCache:     &Cache{},
		},
		{

			name:              "corrupted cache file",
			cacheFileContents: "corrupted cache file",
			opts: &config.SkaffoldOptions{
				CacheArtifacts: true,
			},
			expectedCache: &Cache{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			cacheFile := createTempCacheFile(t, test.cacheFileContents)
			if test.updateCacheFile {
				test.expectedCache.cacheFile = cacheFile
			}
			test.opts.CacheFile = cacheFile

			originalDockerClient := newDockerCilent
			newDockerCilent = func() (docker.LocalDaemon, error) {
				return docker.NewLocalDaemon(test.api, nil), nil
			}
			defer func() {
				newDockerCilent = originalDockerClient
			}()

			if test.updateClient {
				test.expectedCache.client = docker.NewLocalDaemon(test.api, nil)
			}

			actualCache := NewCache(context.Background(), nil, test.opts, test.needsPush)

			// cmp.Diff cannot access unexported fields, so use reflect.DeepEqual here directly
			if !reflect.DeepEqual(test.expectedCache, actualCache) {
				t.Errorf("Expected result different from actual result. Expected: %v, Actual: %v", test.expectedCache, actualCache)
			}
		})
	}
}

func Test_RetrieveCachedArtifacts(t *testing.T) {
	tests := []struct {
		name                 string
		cache                *Cache
		hashes               map[string]string
		artifacts            []*latest.Artifact
		expectedArtifacts    []*latest.Artifact
		api                  testutil.FakeAPIClient
		expectedBuildResults []build.Artifact
	}{
		{
			name:              "useCache is false, return all artifacts",
			cache:             &Cache{},
			artifacts:         []*latest.Artifact{{ImageName: "image1"}},
			expectedArtifacts: []*latest.Artifact{{ImageName: "image1"}},
		},
		{
			name:              "no artifacts in cache",
			cache:             &Cache{useCache: true},
			hashes:            map[string]string{"image1": "hash", "image2": "hash2"},
			artifacts:         []*latest.Artifact{{ImageName: "image1"}, {ImageName: "image2"}},
			expectedArtifacts: []*latest.Artifact{{ImageName: "image1", WorkspaceHash: "hash"}, {ImageName: "image2", WorkspaceHash: "hash2"}},
		},
		{
			name: "one artifact in cache",
			cache: &Cache{
				useCache: true,
				artifactCache: ArtifactCache{"workspace-hash": ImageDetails{
					Digest: "sha256@digest",
				}},
			},
			hashes: map[string]string{"image1": "workspace-hash", "image2": "workspace-hash-2"},
			api: testutil.FakeAPIClient{
				TagToImageID: map[string]string{"image1:workspace-hash": "image1:tag"},
				ImageSummaries: []types.ImageSummary{
					{
						RepoDigests: []string{"sha256@digest"},
						RepoTags:    []string{"image1:workspace-hash"},
					},
				},
			},
			artifacts:            []*latest.Artifact{{ImageName: "image1"}, {ImageName: "image2"}},
			expectedBuildResults: []build.Artifact{{ImageName: "image1", Tag: "image1:workspace-hash"}},
			expectedArtifacts:    []*latest.Artifact{{ImageName: "image2", WorkspaceHash: "workspace-hash-2"}},
		},
		{
			name: "both artifacts in cache, but only one exists locally",
			cache: &Cache{
				useCache: true,
				artifactCache: ArtifactCache{
					"hash":  ImageDetails{Digest: "sha256@digest1"},
					"hash2": ImageDetails{Digest: "sha256@digest2"},
				},
			},
			api: testutil.FakeAPIClient{
				TagToImageID: map[string]string{"image1:hash": "image1:tag"},
				ImageSummaries: []types.ImageSummary{
					{
						ID:          "id",
						RepoDigests: []string{"sha256@digest1"},
						RepoTags:    []string{"image1:hash"},
					},
				},
			},
			hashes:               map[string]string{"image1": "hash", "image2": "hash2"},
			artifacts:            []*latest.Artifact{{ImageName: "image1"}, {ImageName: "image2"}},
			expectedArtifacts:    []*latest.Artifact{{ImageName: "image2", WorkspaceHash: "hash2"}},
			expectedBuildResults: []build.Artifact{{ImageName: "image1", Tag: "image1:hash"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originalHash := hashForArtifact
			hashForArtifact = mockHashForArtifact(test.hashes)
			defer func() {
				hashForArtifact = originalHash
			}()

			test.cache.client = docker.NewLocalDaemon(&test.api, nil)

			actualArtifacts, actualBuildResults := test.cache.RetrieveCachedArtifacts(context.Background(), os.Stdout, test.artifacts)
			testutil.CheckErrorAndDeepEqual(t, false, nil, test.expectedArtifacts, actualArtifacts)
			testutil.CheckErrorAndDeepEqual(t, false, nil, test.expectedBuildResults, actualBuildResults)
		})
	}
}

func createTempCacheFile(t *testing.T, cacheFileContents interface{}) string {
	temp, err := ioutil.TempFile("", "")
	if err != nil {
		t.Fatalf("error creating temp cache file: %v", err)
	}
	defer temp.Close()
	contents, err := yaml.Marshal(cacheFileContents)
	if err != nil {
		t.Fatalf("error marshalling cache: %v", err)
	}
	if err := ioutil.WriteFile(temp.Name(), contents, 0755); err != nil {
		t.Fatalf("error writing contents to %s: %v", temp.Name(), err)
	}
	return temp.Name()
}

func TestRetrieveCachedArtifactDetails(t *testing.T) {
	tests := []struct {
		name                      string
		targetImageExistsRemotely bool
		artifact                  *latest.Artifact
		hashes                    map[string]string
		digest                    string
		api                       testutil.FakeAPIClient
		cache                     *Cache
		expected                  *cachedArtifactDetails
	}{
		{
			name:     "image doesn't exist in cache, remote cluster",
			artifact: &latest.Artifact{ImageName: "image"},
			hashes:   map[string]string{"image": "hash"},
			cache:    noCache,
			expected: &cachedArtifactDetails{
				needsRebuild: true,
			},
		},
		{
			name:     "image doesn't exist in cache, local cluster",
			artifact: &latest.Artifact{ImageName: "image"},
			hashes:   map[string]string{"image": "hash"},
			cache:    noCache,
			expected: &cachedArtifactDetails{
				needsRebuild: true,
			},
		},
		{
			name: "image in cache and exists remotely, remote cluster",
			targetImageExistsRemotely: true,
			artifact:                  &latest.Artifact{ImageName: "image"},
			hashes:                    map[string]string{"image": "hash"},
			api: testutil.FakeAPIClient{
				TagToImageID: map[string]string{"image:hash": "image:tag"},
				ImageSummaries: []types.ImageSummary{
					{
						RepoDigests: []string{"digest"},
						RepoTags:    []string{"image:hash"},
					},
				},
			},
			cache: &Cache{
				useCache:      true,
				artifactCache: ArtifactCache{"hash": ImageDetails{Digest: "digest"}},
			},
			digest: "digest",
			expected: &cachedArtifactDetails{
				hashTag:       "image:hash",
				prebuiltImage: "image:hash",
			},
		},
		{
			name:     "image in cache and exists in daemon, local cluster",
			artifact: &latest.Artifact{ImageName: "image"},
			hashes:   map[string]string{"image": "hash"},
			api: testutil.FakeAPIClient{
				TagToImageID: map[string]string{"image:hash": "image:tag"},
				ImageSummaries: []types.ImageSummary{
					{
						RepoDigests: []string{"digest"},
						RepoTags:    []string{"image:hash"},
					},
				},
			},
			cache: &Cache{
				useCache:      true,
				localCluster:  true,
				artifactCache: ArtifactCache{"hash": ImageDetails{Digest: "digest"}},
			},
			digest: "digest",
			expected: &cachedArtifactDetails{
				hashTag:       "image:hash",
				prebuiltImage: "image:hash",
			},
		},
		{
			name:     "image in cache, prebuilt image exists, remote cluster",
			artifact: &latest.Artifact{ImageName: "image"},
			hashes:   map[string]string{"image": "hash"},
			cache: &Cache{
				useCache:      true,
				artifactCache: ArtifactCache{"hash": ImageDetails{Digest: digest}},
				imageList: []types.ImageSummary{
					{
						RepoDigests: []string{fmt.Sprintf("image@%s", digest)},
						RepoTags:    []string{"anotherimage:hash"},
					},
				},
			},
			digest: digest,
			expected: &cachedArtifactDetails{
				needsRetag:    true,
				needsPush:     true,
				prebuiltImage: "anotherimage:hash",
				hashTag:       "image:hash",
			},
		},
		{
			name:     "image in cache, prebuilt image exists, local cluster",
			artifact: &latest.Artifact{ImageName: "image"},
			hashes:   map[string]string{"image": "hash"},
			cache: &Cache{
				useCache:      true,
				localCluster:  true,
				artifactCache: ArtifactCache{"hash": ImageDetails{Digest: digest}},
				imageList: []types.ImageSummary{
					{
						RepoDigests: []string{fmt.Sprintf("image@%s", digest)},
						RepoTags:    []string{"anotherimage:hash"},
					},
				},
			},
			digest: digest,
			expected: &cachedArtifactDetails{
				needsRetag:    true,
				prebuiltImage: "anotherimage:hash",
				hashTag:       "image:hash",
			},
		},
		{
			name: "push specified, local cluster, image exists remotely",
			targetImageExistsRemotely: true,
			artifact:                  &latest.Artifact{ImageName: "image"},
			hashes:                    map[string]string{"image": "hash"},
			cache: &Cache{
				useCache:      true,
				needsPush:     true,
				localCluster:  true,
				artifactCache: ArtifactCache{"hash": ImageDetails{Digest: digest}},
				imageList: []types.ImageSummary{
					{
						RepoDigests: []string{fmt.Sprintf("image@%s", digest)},
						RepoTags:    []string{"anotherimage:hash"},
					},
				},
			},
			digest: digest,
			expected: &cachedArtifactDetails{
				needsRetag:    true,
				prebuiltImage: "anotherimage:hash",
				hashTag:       "image:hash",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originalHash := hashForArtifact
			hashForArtifact = mockHashForArtifact(test.hashes)
			defer func() {
				hashForArtifact = originalHash
			}()

			originalRemoteDigest := remoteDigest
			remoteDigest = func(string) (string, error) {
				return test.digest, nil
			}
			defer func() {
				remoteDigest = originalRemoteDigest
			}()

			originalImgExistsRemotely := imgExistsRemotely
			imgExistsRemotely = func(_, _ string) bool {
				return test.targetImageExistsRemotely
			}
			defer func() {
				imgExistsRemotely = originalImgExistsRemotely
			}()

			test.cache.client = docker.NewLocalDaemon(&test.api, nil)
			actual, err := test.cache.retrieveCachedArtifactDetails(context.Background(), test.artifact)
			if err != nil {
				t.Fatalf("error retrieving artifact details: %v", err)
			}
			// cmp.Diff cannot access unexported fields, so use reflect.DeepEqual here directly
			if !reflect.DeepEqual(test.expected, actual) {
				t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
			}
		})
	}
}

func TestRetrievePrebuiltImage(t *testing.T) {
	tests := []struct {
		name         string
		cache        *Cache
		imageDetails ImageDetails
		shouldErr    bool
		expected     string
	}{
		{
			name: "one image id exists",
			cache: &Cache{
				imageList: []types.ImageSummary{
					{
						RepoTags:    []string{"image:mytag"},
						RepoDigests: []string{image},
					},
					{
						RepoTags:    []string{"image1:latest"},
						RepoDigests: []string{imageOne},
					},
				},
			},
			imageDetails: ImageDetails{
				Digest: digest,
			},
			expected: "image:mytag",
		},
		{
			name: "no image id exists",
			cache: &Cache{
				imageList: []types.ImageSummary{
					{
						RepoTags:    []string{"image:mytag"},
						RepoDigests: []string{image},
					},
					{
						RepoTags:    []string{"image:mytag"},
						RepoDigests: []string{image},
					},
				},
			},
			shouldErr: true,
			imageDetails: ImageDetails{
				Digest: "dne",
			},
			expected: "",
		},
		{
			name: "one image id exists",
			cache: &Cache{
				imageList: []types.ImageSummary{
					{
						RepoTags: []string{"image1", "image2"},
						ID:       "something",
					},
					{
						RepoTags: []string{"image3"},
						ID:       "imageid",
					},
				},
			},
			imageDetails: ImageDetails{
				ID: "imageid",
			},
			expected: "image3",
		},
		{
			name: "multiple image ids exist",
			cache: &Cache{
				imageList: []types.ImageSummary{
					{
						RepoTags: []string{"image1", "image2"},
						ID:       "something",
					},
					{
						RepoTags: []string{"image3", "image4"},
						ID:       "imageid",
					},
				},
			},
			imageDetails: ImageDetails{
				ID: "imageid",
			},
			expected: "image3",
		},
		{
			name: "no image id exists",
			cache: &Cache{
				imageList: []types.ImageSummary{
					{
						RepoTags: []string{"image1", "image2"},
						ID:       "something",
					},
					{
						RepoTags: []string{"image3"},
						ID:       "somethingelse",
					},
				},
			},
			imageDetails: ImageDetails{
				ID: "imageid",
			},
			shouldErr: true,
			expected:  "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := test.cache.retrievePrebuiltImage(test.imageDetails)
			testutil.CheckErrorAndDeepEqual(t, test.shouldErr, err, test.expected, actual)
		})
	}
}
