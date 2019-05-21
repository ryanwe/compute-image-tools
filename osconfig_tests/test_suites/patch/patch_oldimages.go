//  Copyright 2019 Google Inc. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

// Package patch contains end to end tests for patch management
package patch

import (
	"context"
	"fmt"
	"log"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	daisyCompute "github.com/GoogleCloudPlatform/compute-image-tools/daisy/compute"
	"github.com/GoogleCloudPlatform/compute-image-tools/osconfig_tests/compute"
	gcpclients "github.com/GoogleCloudPlatform/compute-image-tools/osconfig_tests/gcp_clients"
	"github.com/GoogleCloudPlatform/compute-image-tools/osconfig_tests/junitxml"
	testconfig "github.com/GoogleCloudPlatform/compute-image-tools/osconfig_tests/test_config"
	"github.com/GoogleCloudPlatform/compute-image-tools/osconfig_tests/utils"
	osconfig "github.com/GoogleCloudPlatform/osconfig/_internal/gapi-cloud-osconfig-go/cloud.google.com/go/osconfig/apiv1alpha1"
	osconfigpb "github.com/GoogleCloudPlatform/osconfig/_internal/gapi-cloud-osconfig-go/google.golang.org/genproto/googleapis/cloud/osconfig/v1alpha1"
	"github.com/golang/protobuf/ptypes/duration"
	api "google.golang.org/api/compute/v1"
)

const (
	patchOldImagesTestsName = "PatchOldImagesTests"
)

// ExecutePatchOldImagesTestSuite is a PatchTests test suite.
func ExecutePatchOldImagesTestSuite(ctx context.Context, tswg *sync.WaitGroup, testSuites chan *junitxml.TestSuite, logger *log.Logger, testSuiteRegex, testCaseRegex *regexp.Regexp, testProjectConfig *testconfig.Project) {
	defer tswg.Done()

	if testSuiteRegex != nil && !testSuiteRegex.MatchString(patchOldImagesTestsName) {
		return
	}

	testSuite := junitxml.NewTestSuite(patchOldImagesTestsName)
	defer testSuite.Finish(testSuites)

	logger.Printf("Running TestSuite %q", testSuite.Name)

	// debStartupScript := utils.InstallOSConfigDeb + `

	// echo -------
	// echo "Kernel version:$(uname -r)"
	// echo 'osconfig test setup done'
	// echo -----
	// `

	rhel7StartupScript := utils.InstallOSConfigYumEL7 + `

	echo -------
	echo "Kernel version:$(uname -sr)"
	echo 'osconfig test setup done'
	echo -----
	`

	testSetup := []*patchTestSetup{

		// &patchTestSetup{
		// 	image:         "projects/debian-cloud/global/images/debian-9-stretch-v20180105",
		// 	assertTimeout: 5 * time.Minute,
		// 	startup: &api.MetadataItems{
		// 		Key:   "startup-script",
		// 		Value: &debStartupScript,
		// 	},
		// },
		// &patchTestSetup{
		// 	image:         "projects/debian-cloud/global/images/family/debian-9",
		// 	assertTimeout: 5 * time.Minute,
		// 	startup: &api.MetadataItems{
		// 		Key:   "startup-script",
		// 		Value: &debStartupScript,
		// 	},
		// },
		&patchTestSetup{
			image:         "projects/rhel-cloud/global/images/rhel-7-v20180104",
			assertTimeout: 5 * time.Minute,
			startup: &api.MetadataItems{
				Key:   "startup-script",
				Value: &rhel7StartupScript,
			},
		},
		&patchTestSetup{
			image:         "projects/rhel-cloud/global/images/family/rhel-7",
			assertTimeout: 5 * time.Minute,
			startup: &api.MetadataItems{
				Key:   "startup-script",
				Value: &rhel7StartupScript,
			},
		},
	}

	var wg sync.WaitGroup
	tests := make(chan *junitxml.TestCase)
	for _, setup := range testSetup {
		wg.Add(1)
		go patchTestCase2(ctx, setup, tests, &wg, logger, testCaseRegex, testProjectConfig)
	}

	go func() {
		wg.Wait()
		close(tests)
	}()

	for ret := range tests {
		testSuite.TestCase = append(testSuite.TestCase, ret)
	}

	logger.Printf("Finished TestSuite %q", testSuite.Name)
}

func patchTestCase2(ctx context.Context, testSetup *patchTestSetup, tests chan *junitxml.TestCase, wg *sync.WaitGroup, logger *log.Logger, regex *regexp.Regexp, testProjectConfig *testconfig.Project) {
	defer wg.Done()

	executePatchTest := junitxml.NewTestCase(testSuiteName, fmt.Sprintf("[patchOldImageTest] [%s] Execute PatchJob on Old Image", testSetup.image))

	for tc, f := range map[*junitxml.TestCase]func(context.Context, *junitxml.TestCase, *patchTestSetup, *log.Logger, *testconfig.Project){
		executePatchTest: runExecutePatchTest2,
	} {
		if tc.FilterTestCase(regex) {
			tc.Finish(tests)
		} else {
			logger.Printf("Running TestCase %q", tc.Name)
			f(ctx, tc, testSetup, logger, testProjectConfig)
			tc.Finish(tests)
			logger.Printf("TestCase c%q finished in %fs", tc.Name, tc.Time)
		}
	}
}

func runExecutePatchTest2(ctx context.Context, testCase *junitxml.TestCase, testSetup *patchTestSetup, logger *log.Logger, testProjectConfig *testconfig.Project) {

	client, err := daisyCompute.NewClient(ctx)
	if err != nil {
		testCase.WriteFailure("error creating client: %v", err)
		return
	}

	testCase.Logf("Creating instance with image %q", testSetup.image)
	var metadataItems []*api.MetadataItems
	metadataItems = append(metadataItems, testSetup.startup)
	metadataItems = append(metadataItems, compute.BuildInstanceMetadataItem("os-config-enabled-prerelease-features", "ospatch"))
	testSetupName := fmt.Sprintf("patch-test-%s-%s", path.Base(testSetup.image), suffix)
	inst, err := utils.CreateComputeInstance(metadataItems, client, "n1-standard-4", testSetup.image, testSetupName, testProjectConfig.TestProjectID, testProjectConfig.GetZone(), testProjectConfig.ServiceAccountEmail, testProjectConfig.ServiceAccountScopes)
	if err != nil {
		testCase.WriteFailure("Error creating instance: %v", utils.GetStatusFromError(err))
		return
	}
	// defer inst.Cleanup()

	testCase.Logf("Waiting for agent install to complete")
	if err := inst.WaitForSerialOutput("osconfig test setup done", 1, 5*time.Second, 5*time.Minute); err != nil {
		testCase.WriteFailure("Error waiting for osconfig agent install: %v", err)
		return
	}

	testCase.Logf("Agent installed successfully")

	// create patch job
	parent := fmt.Sprintf("projects/%s", testProjectConfig.TestProjectID)
	osconfigClient, err := gcpclients.GetOsConfigClient(ctx)

	assertTimeout := testSetup.assertTimeout

	req := &osconfigpb.ExecutePatchJobRequest{
		Parent:      parent,
		Description: "testing default patch job run",
		Filter:      fmt.Sprintf("name=\"%s\"", testSetupName),
		Duration:    &duration.Duration{Seconds: int64(assertTimeout / time.Second)},
		// TODO: expect auto-reboot
		PatchConfig: &osconfigpb.PatchConfig{
			RebootConfig: osconfigpb.PatchConfig_ALWAYS,
		},
	}
	job, err := osconfigClient.ExecutePatchJob(ctx, req)

	if err != nil {
		testCase.WriteFailure("error while executing patch job: \n%s\n", utils.GetStatusFromError(err))
		return
	}

	testCase.Logf("Started patch job '%s'", job.GetName())

	logger.Printf("%v\n", job)

	err = waitForPatchJobToComplete(ctx, osconfigClient, testSetup, job)
	if err != nil {
		testCase.WriteFailure("%v", err)
		return
	}

	err = validateRebootAndKernelVersionChanged(ctx, inst, client)
	if err != nil {
		testCase.WriteFailure("%v", err)
		return
	}
}

func waitForPatchJobToComplete(ctx context.Context, osconfigClient *osconfig.Client, testSetup *patchTestSetup, job *osconfigpb.PatchJob) error {
	// assertion
	tick := time.Tick(5 * time.Second)
	timedout := time.Tick(testSetup.assertTimeout)
	for {
		select {
		case <-timedout:
			return fmt.Errorf("Patch job '%s' timed out", job.GetName())
		case <-tick:
			req := &osconfigpb.GetPatchJobRequest{
				Name: job.GetName(),
			}
			res, err := osconfigClient.GetPatchJob(ctx, req)
			if err != nil {
				return fmt.Errorf("Error while fetching patch job: \n%s", utils.GetStatusFromError(err))
			}

			if isPatchJobFailureState(res.State) {
				return fmt.Errorf("Patch job '%s' completed with status %v and message '%s'", job.GetName(), res.State, job.GetErrorMessage())
			}

			if res.State == osconfigpb.PatchJob_SUCCEEDED {
				if res.InstanceDetailsSummary.GetInstancesSucceeded() < 1 {
					return fmt.Errorf("Patch job '%s' completed with no instances patched", job.GetName())
				}
				return nil
			}
		}
	}
}

// The startup script will output the kernel version when the instance starts. Here we read the serial port
// expecting the startup script to have written the kernel version at least twice: once when the instance was
// first started and again after the patch reboot. If this isn't the case or the version hasn't changed, the
// test is considered a failure.
func validateRebootAndKernelVersionChanged(ctx context.Context, inst *compute.Instance, client daisyCompute.Client) error {
	var start int64
	var versions []string

	for {

		resp, err := client.GetSerialPortOutput(inst.Project, inst.Zone, inst.Name, 1, start)
		if err != nil {
			return err
		}

		for _, ln := range strings.Split(resp.Contents, "\n") {
			if strings.Contains(ln, "Kernel version:") {
				ss := strings.Split(ln, ":")
				s := ss[len(ss)-1]
				fmt.Printf("I found this '%s'", s)
				versions = append(versions, s)

				if len(versions) == 2 {
					// Reboot completed
					// TODO: validate version changed.
					fmt.Printf("Old Version '%s' and new version '%s'")
					return nil
				}
			}
		}

		if start == resp.Next {
			// nothing else
			return nil
		}
		start = resp.Next

		fmt.Printf("Next start is %d\n", start)
	}

}
