/*
Copyright 2026 Fabien Dupont.

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

package controller

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fabiendupont/cluster-api-provider-nvidia-carbide/internal/controller/testutil"
	bmm "github.com/nvidia/bare-metal-manager-rest/sdk/standard"
)

var _ = Describe("NvidiaCarbideMachine Controller", func() {
	Context("When reconciling instance creation", func() {
		It("should create instance with correct parameters", func() {
			instanceID := uuid.New().String()
			mockClient := &testutil.MockCarbideClient{
				CreateInstanceFunc: func(
					ctx context.Context, org string, req bmm.InstanceCreateRequest,
				) (*bmm.Instance, *http.Response, error) {
					Expect(org).To(Equal("test-org"))
					Expect(req.Name).To(Equal("test-machine"))

					return &bmm.Instance{
						Id:   &instanceID,
						Name: testutil.Ptr("test-machine"),
					}, testutil.MockHTTPResponse(201), nil
				},
			}

			_ = mockClient
			// TODO: Implement full test with controller reconciliation
		})
	})
})
