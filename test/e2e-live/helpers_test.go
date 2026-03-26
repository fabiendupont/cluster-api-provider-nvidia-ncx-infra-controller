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

//go:build e2e

package e2e_live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrastructurev1beta1 "github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/api/v1beta1"
)

const (
	keycloakRealm        = "carbide-dev"
	keycloakClientID     = "carbide-api"
	keycloakClientSecret = "carbide-local-secret"
	keycloakUsername      = "admin@example.com"
	keycloakPassword     = "adminpassword"
)

// getKeycloakToken acquires a JWT from Keycloak using the resource owner password grant.
func getKeycloakToken() string {
	keycloakURL := os.Getenv("NCX_INFRA_KEYCLOAK_URL")
	Expect(keycloakURL).NotTo(BeEmpty(), "NCX_INFRA_KEYCLOAK_URL must be set")

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", keycloakURL, keycloakRealm)

	data := url.Values{
		"grant_type":    {"password"},
		"client_id":     {keycloakClientID},
		"client_secret": {keycloakClientSecret},
		"username":      {keycloakUsername},
		"password":      {keycloakPassword},
	}

	resp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	Expect(err).NotTo(HaveOccurred(), "Failed to request Keycloak token")
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK),
		"Keycloak token request failed with status %d: %s", resp.StatusCode, string(body))

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	Expect(json.Unmarshal(body, &tokenResp)).To(Succeed())
	Expect(tokenResp.AccessToken).NotTo(BeEmpty(), "Received empty access token from Keycloak")

	_, _ = fmt.Fprintf(GinkgoWriter, "Successfully acquired Keycloak token\n")
	return tokenResp.AccessToken
}

// createCredentialsSecret creates a Kubernetes secret with NVIDIA Carbide API credentials.
func createCredentialsSecret(ctx context.Context, k8sClient client.Client, name, namespace, token string) *corev1.Secret {
	// Use the in-cluster endpoint if available (for controllers running inside the cluster),
	// otherwise fall back to the external endpoint.
	endpoint := os.Getenv("NCX_INFRA_API_ENDPOINT_INTERNAL")
	if endpoint == "" {
		endpoint = os.Getenv("NCX_INFRA_API_ENDPOINT")
	}
	Expect(endpoint).NotTo(BeEmpty(), "NCX_INFRA_API_ENDPOINT or NCX_INFRA_API_ENDPOINT_INTERNAL must be set")

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"endpoint": []byte(endpoint),
			"orgName":  []byte("test-org"),
			"token":    []byte(token),
		},
	}
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	_, _ = fmt.Fprintf(GinkgoWriter, "Created credentials secret %s/%s\n", namespace, name)
	return secret
}

// waitForClusterReady polls the NcxInfraCluster status until Ready is true.
func waitForClusterReady(ctx context.Context, k8sClient client.Client, cluster *infrastructurev1beta1.NcxInfraCluster) {
	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)
		if err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "Error getting cluster: %v\n", err)
			return false
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "Cluster ready=%v, vpcID=%s\n", cluster.Status.Ready, cluster.Status.VPCID)
		return cluster.Status.Ready
	}, clusterCreationTimeout, pollInterval).Should(BeTrue(), "NcxInfraCluster did not become ready")
}

// waitForMachineReady polls the NcxInfraMachine status until Ready is true.
func waitForMachineReady(ctx context.Context, k8sClient client.Client, machine *infrastructurev1beta1.NcxInfraMachine) {
	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(machine), machine)
		if err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "Error getting machine: %v\n", err)
			return false
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "Machine ready=%v, instanceID=%s, state=%s\n",
			machine.Status.Ready, machine.Status.InstanceID, machine.Status.InstanceState)
		return machine.Status.Ready
	}, clusterCreationTimeout, pollInterval).Should(BeTrue(), "NcxInfraMachine did not become ready")
}

// ncxInfraAPIRequest makes an authenticated request to the Carbide REST API.
func ncxInfraAPIRequest(method, path, token string, body interface{}) (map[string]interface{}, int) {
	endpoint := os.Getenv("NCX_INFRA_API_ENDPOINT")
	Expect(endpoint).NotTo(BeEmpty())

	var reqBody io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		reqBody = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequest(method, endpoint+path, reqBody)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())

	var result map[string]interface{}
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &result)
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "%s %s -> %d\n", method, path, resp.StatusCode)
	return result, resp.StatusCode
}

// getExistingSiteID finds the local-dev-site created by setup-local.sh.
// VPC creation triggers a Temporal workflow that requires a site-agent — only
// the pre-provisioned local-dev-site has one connected.
func getExistingSiteID(token, orgName string) string {
	apiBase := fmt.Sprintf("/v2/org/%s/carbide", orgName)

	result, status := ncxInfraAPIRequest("GET", apiBase+"/site", token, nil)
	Expect(status).To(Equal(http.StatusOK), "Failed to list sites: %v", result)

	// The response is an array — parse from raw response
	endpoint := os.Getenv("NCX_INFRA_API_ENDPOINT")
	req, err := http.NewRequest("GET", endpoint+apiBase+"/site", nil)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())

	var sites []map[string]interface{}
	Expect(json.Unmarshal(body, &sites)).To(Succeed())
	Expect(sites).NotTo(BeEmpty(), "No sites found — was setup-local.sh run?")

	siteID := sites[0]["id"].(string)
	siteName := sites[0]["name"].(string)
	_, _ = fmt.Fprintf(GinkgoWriter, "Using existing site %s (id=%s)\n", siteName, siteID)
	return siteID
}
// ensureSiteRegistered ensures the site is in Registered state.
// The site-agent registration workflow may not have completed yet.
func ensureSiteRegistered(siteID string) {
	cmd := exec.Command("kubectl", "exec", "-n", "postgres", "statefulset/postgres", "--",
		"psql", "-U", "forge", "-d", "forge", "-c",
		fmt.Sprintf("UPDATE site SET status = 'Registered' WHERE id = '%s' AND status != 'Registered'", siteID))
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to ensure site is registered: %s", string(output))
	_, _ = fmt.Fprintf(GinkgoWriter, "Ensured site %s is Registered\n", siteID)
}

// enableTargetedInstanceCreation enables the TargetedInstanceCreation capability on the tenant.
func enableTargetedInstanceCreation(tenantID string) {
	cmd := exec.Command("kubectl", "exec", "-n", "postgres", "statefulset/postgres", "--",
		"psql", "-U", "forge", "-d", "forge", "-c",
		fmt.Sprintf("UPDATE tenant SET config = COALESCE(config, '{}')::jsonb || '{\"targetedInstanceCreation\": true}'::jsonb WHERE id = '%s'", tenantID))
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to enable targeted instance creation: %s", string(output))
	_, _ = fmt.Fprintf(GinkgoWriter, "Enabled TargetedInstanceCreation for tenant %s\n", tenantID)
}

// ensureSubnetReady ensures the subnet is in Ready state.
func ensureSubnetReady(subnetID string) {
	cmd := exec.Command("kubectl", "exec", "-n", "postgres", "statefulset/postgres", "--",
		"psql", "-U", "forge", "-d", "forge", "-c",
		fmt.Sprintf("UPDATE subnet SET status = 'Ready' WHERE id = '%s' AND status != 'Ready'", subnetID))
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to ensure subnet is ready: %s", string(output))
	_, _ = fmt.Fprintf(GinkgoWriter, "Ensured subnet %s is Ready\n", subnetID)
}

// getInfraProviderID retrieves the infrastructure provider ID for the org.
func getInfraProviderID(token, orgName string) string {
	apiBase := fmt.Sprintf("/v2/org/%s/carbide", orgName)
	result, status := ncxInfraAPIRequest("GET", apiBase+"/infrastructure-provider/current", token, nil)
	Expect(status).To(Equal(http.StatusOK), "Failed to get infrastructure provider: %v", result)
	id := result["id"].(string)
	_, _ = fmt.Fprintf(GinkgoWriter, "Infrastructure Provider ID: %s\n", id)
	return id
}

// createTestMachineInDB inserts a test machine directly into PostgreSQL.
func createTestMachineInDB(siteID, infraProviderID, machineID string) {
	sql := fmt.Sprintf(
		"INSERT INTO machine (id, infrastructure_provider_id, site_id, controller_machine_id, status, is_in_maintenance, is_usable_by_tenant, is_network_degraded, is_assigned, is_missing_on_site, created, updated) "+
			"VALUES ('%s', '%s', '%s', '%s', 'Ready', false, true, false, false, false, NOW(), NOW()) ON CONFLICT (id) DO NOTHING",
		machineID, infraProviderID, siteID, machineID)
	cmd := exec.Command("kubectl", "exec", "-n", "postgres", "statefulset/postgres", "--",
		"psql", "-U", "forge", "-d", "forge", "-c", sql)
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to create test machine in DB: %s", string(output))
	_, _ = fmt.Fprintf(GinkgoWriter, "Created test machine %s in DB\n", machineID)
}

// setupSiteViaAPI finds the existing site and creates the tenant resources needed
// for the cluster controller: Tenant -> IP Block -> Allocation + test machine in DB.
// Returns siteID, tenantID, and machineID.
func setupSiteViaAPI(token, orgName, prefix string) (siteID, tenantID, machineID string) {
	apiBase := fmt.Sprintf("/v2/org/%s/carbide", orgName)

	// Use the existing site (has a connected site-agent for Temporal workflows)
	siteID = getExistingSiteID(token, orgName)

	// Ensure site is in Registered state (the site-agent registration may not have completed yet)
	ensureSiteRegistered(siteID)

	// Get or create Tenant (idempotent)
	ncxInfraAPIRequest("POST", apiBase+"/tenant", token, map[string]interface{}{"org": orgName})
	currentTenant, tStatus := ncxInfraAPIRequest("GET", apiBase+"/tenant/current", token, nil)
	Expect(tStatus).To(Equal(http.StatusOK), "Failed to get current tenant: %v", currentTenant)
	tenantID = currentTenant["id"].(string)
	_, _ = fmt.Fprintf(GinkgoWriter, "Tenant ID: %s\n", tenantID)
	enableTargetedInstanceCreation(tenantID)

	// Note: IP block and allocation are created by the cluster controller
	// in ensureIPBlockAndAllocation(). We only need the test machine here.

	// Create a test machine in DB (mock-core doesn't persist machines)
	infraProviderID := getInfraProviderID(token, orgName)
	machineID = prefix + "-machine"
	createTestMachineInDB(siteID, infraProviderID, machineID)

	return siteID, tenantID, machineID
}
