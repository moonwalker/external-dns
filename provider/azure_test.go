/*
Copyright 2017 The Kubernetes Authors.

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

package provider

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/arm/dns"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/to"

	"github.com/moonwalker/external-dns/endpoint"
	"github.com/moonwalker/external-dns/internal/testutils"
	"github.com/moonwalker/external-dns/plan"
	"github.com/stretchr/testify/assert"
)

type mockZonesClient struct {
	mockZoneListResult *dns.ZoneListResult
}

type mockRecordsClient struct {
	mockRecordSet    *[]dns.RecordSet
	deletedEndpoints []*endpoint.Endpoint
	updatedEndpoints []*endpoint.Endpoint
}

func createMockZone(zone string, id string) dns.Zone {
	return dns.Zone{
		ID:   to.StringPtr(id),
		Name: to.StringPtr(zone),
	}
}

func (client *mockZonesClient) ListByResourceGroup(resourceGroupName string, top *int32) (dns.ZoneListResult, error) {
	// Don't bother filtering by resouce group or implementing paging since that's the responsibility
	// of the Azure DNS service
	return *client.mockZoneListResult, nil
}

func (client *mockZonesClient) ListByResourceGroupNextResults(lastResults dns.ZoneListResult) (dns.ZoneListResult, error) {
	return dns.ZoneListResult{}, nil
}

func aRecordSetPropertiesGetter(value string, ttl int64) *dns.RecordSetProperties {
	return &dns.RecordSetProperties{
		TTL: to.Int64Ptr(ttl),
		ARecords: &[]dns.ARecord{
			{
				Ipv4Address: to.StringPtr(value),
			},
		},
	}
}

func cNameRecordSetPropertiesGetter(value string, ttl int64) *dns.RecordSetProperties {
	return &dns.RecordSetProperties{
		TTL: to.Int64Ptr(ttl),
		CnameRecord: &dns.CnameRecord{
			Cname: to.StringPtr(value),
		},
	}
}

func txtRecordSetPropertiesGetter(value string, ttl int64) *dns.RecordSetProperties {
	return &dns.RecordSetProperties{
		TTL: to.Int64Ptr(ttl),
		TxtRecords: &[]dns.TxtRecord{
			{
				Value: &[]string{value},
			},
		},
	}
}

func othersRecordSetPropertiesGetter(value string, ttl int64) *dns.RecordSetProperties {
	return &dns.RecordSetProperties{
		TTL: to.Int64Ptr(ttl),
	}
}
func createMockRecordSet(name, recordType, value string) dns.RecordSet {
	return createMockRecordSetWithTTL(name, recordType, value, 0)
}
func createMockRecordSetWithTTL(name, recordType, value string, ttl int64) dns.RecordSet {
	var getterFunc func(value string, ttl int64) *dns.RecordSetProperties

	switch recordType {
	case endpoint.RecordTypeA:
		getterFunc = aRecordSetPropertiesGetter
	case endpoint.RecordTypeCNAME:
		getterFunc = cNameRecordSetPropertiesGetter
	case endpoint.RecordTypeTXT:
		getterFunc = txtRecordSetPropertiesGetter
	default:
		getterFunc = othersRecordSetPropertiesGetter
	}
	return dns.RecordSet{
		Name:                to.StringPtr(name),
		Type:                to.StringPtr("Microsoft.Network/dnszones/" + recordType),
		RecordSetProperties: getterFunc(value, ttl),
	}

}

func (client *mockRecordsClient) ListByDNSZone(resourceGroupName string, zoneName string, top *int32) (dns.RecordSetListResult, error) {
	return dns.RecordSetListResult{Value: client.mockRecordSet}, nil
}

func (client *mockRecordsClient) ListByDNSZoneNextResults(list dns.RecordSetListResult) (dns.RecordSetListResult, error) {
	return dns.RecordSetListResult{}, nil
}

func (client *mockRecordsClient) Delete(resourceGroupName string, zoneName string, relativeRecordSetName string, recordType dns.RecordType, ifMatch string) (autorest.Response, error) {
	client.deletedEndpoints = append(
		client.deletedEndpoints,
		endpoint.NewEndpoint(
			formatAzureDNSName(relativeRecordSetName, zoneName),
			"",
			string(recordType),
		),
	)
	return autorest.Response{}, nil
}

func (client *mockRecordsClient) CreateOrUpdate(resourceGroupName string, zoneName string, relativeRecordSetName string, recordType dns.RecordType, parameters dns.RecordSet, ifMatch string, ifNoneMatch string) (dns.RecordSet, error) {
	var ttl endpoint.TTL
	if parameters.TTL != nil {
		ttl = endpoint.TTL(*parameters.TTL)
	}
	client.updatedEndpoints = append(
		client.updatedEndpoints,
		endpoint.NewEndpointWithTTL(
			formatAzureDNSName(relativeRecordSetName, zoneName),
			extractAzureTarget(&parameters),
			string(recordType),
			ttl,
		),
	)
	return parameters, nil
}

func newAzureProvider(domainFilter DomainFilter, zoneIDFilter ZoneIDFilter, dryRun bool, resourceGroup string, zonesClient ZonesClient, recordsClient RecordsClient) *AzureProvider {
	return &AzureProvider{
		domainFilter:  domainFilter,
		zoneIDFilter:  zoneIDFilter,
		dryRun:        dryRun,
		resourceGroup: resourceGroup,
		zonesClient:   zonesClient,
		recordsClient: recordsClient,
	}
}

func validateAzureEndpoints(t *testing.T, endpoints []*endpoint.Endpoint, expected []*endpoint.Endpoint) {
	assert.True(t, testutils.SameEndpoints(endpoints, expected), "expected and actual endpoints don't match. %s:%s", endpoints, expected)
}

func TestAzureRecord(t *testing.T) {
	zonesClient := mockZonesClient{
		mockZoneListResult: &dns.ZoneListResult{
			Value: &[]dns.Zone{
				createMockZone("example.com", "/dnszones/example.com"),
			},
		},
	}

	recordsClient := mockRecordsClient{
		mockRecordSet: &[]dns.RecordSet{
			createMockRecordSet("@", "NS", "ns1-03.azure-dns.com."),
			createMockRecordSet("@", "SOA", "Email: azuredns-hostmaster.microsoft.com"),
			createMockRecordSet("@", endpoint.RecordTypeA, "123.123.123.122"),
			createMockRecordSet("@", endpoint.RecordTypeTXT, "heritage=external-dns,external-dns/owner=default"),
			createMockRecordSetWithTTL("nginx", endpoint.RecordTypeA, "123.123.123.123", 3600),
			createMockRecordSetWithTTL("nginx", endpoint.RecordTypeTXT, "heritage=external-dns,external-dns/owner=default", recordTTL),
			createMockRecordSetWithTTL("hack", endpoint.RecordTypeCNAME, "hack.azurewebsites.net", 10),
		},
	}

	provider := newAzureProvider(NewDomainFilter([]string{"example.com"}), NewZoneIDFilter([]string{""}), true, "k8s", &zonesClient, &recordsClient)

	actual, err := provider.Records()

	if err != nil {
		t.Fatal(err)
	}
	expected := []*endpoint.Endpoint{
		endpoint.NewEndpoint("example.com", "123.123.123.122", endpoint.RecordTypeA),
		endpoint.NewEndpoint("example.com", "heritage=external-dns,external-dns/owner=default", endpoint.RecordTypeTXT),
		endpoint.NewEndpointWithTTL("nginx.example.com", "123.123.123.123", endpoint.RecordTypeA, 3600),
		endpoint.NewEndpointWithTTL("nginx.example.com", "heritage=external-dns,external-dns/owner=default", endpoint.RecordTypeTXT, recordTTL),
		endpoint.NewEndpointWithTTL("hack.example.com", "hack.azurewebsites.net", endpoint.RecordTypeCNAME, 10),
	}

	validateAzureEndpoints(t, actual, expected)

}

func TestAzureApplyChanges(t *testing.T) {
	recordsClient := mockRecordsClient{}

	testAzureApplyChangesInternal(t, false, &recordsClient)

	validateAzureEndpoints(t, recordsClient.deletedEndpoints, []*endpoint.Endpoint{
		endpoint.NewEndpoint("old.example.com", "", endpoint.RecordTypeA),
		endpoint.NewEndpoint("oldcname.example.com", "", endpoint.RecordTypeCNAME),
		endpoint.NewEndpoint("deleted.example.com", "", endpoint.RecordTypeA),
		endpoint.NewEndpoint("deletedcname.example.com", "", endpoint.RecordTypeCNAME),
	})

	validateAzureEndpoints(t, recordsClient.updatedEndpoints, []*endpoint.Endpoint{
		endpoint.NewEndpointWithTTL("example.com", "1.2.3.4", endpoint.RecordTypeA, endpoint.TTL(recordTTL)),
		endpoint.NewEndpointWithTTL("example.com", "tag", endpoint.RecordTypeTXT, endpoint.TTL(recordTTL)),
		endpoint.NewEndpointWithTTL("foo.example.com", "1.2.3.4", endpoint.RecordTypeA, endpoint.TTL(recordTTL)),
		endpoint.NewEndpointWithTTL("foo.example.com", "tag", endpoint.RecordTypeTXT, endpoint.TTL(recordTTL)),
		endpoint.NewEndpointWithTTL("bar.example.com", "other.com", endpoint.RecordTypeCNAME, endpoint.TTL(recordTTL)),
		endpoint.NewEndpointWithTTL("bar.example.com", "tag", endpoint.RecordTypeTXT, endpoint.TTL(recordTTL)),
		endpoint.NewEndpointWithTTL("other.com", "5.6.7.8", endpoint.RecordTypeA, endpoint.TTL(recordTTL)),
		endpoint.NewEndpointWithTTL("other.com", "tag", endpoint.RecordTypeTXT, endpoint.TTL(recordTTL)),
		endpoint.NewEndpointWithTTL("new.example.com", "111.222.111.222", endpoint.RecordTypeA, 3600),
		endpoint.NewEndpointWithTTL("newcname.example.com", "other.com", endpoint.RecordTypeCNAME, 10),
	})
}

func TestAzureApplyChangesDryRun(t *testing.T) {
	recordsClient := mockRecordsClient{}

	testAzureApplyChangesInternal(t, true, &recordsClient)

	validateAzureEndpoints(t, recordsClient.deletedEndpoints, []*endpoint.Endpoint{})

	validateAzureEndpoints(t, recordsClient.updatedEndpoints, []*endpoint.Endpoint{})
}

func testAzureApplyChangesInternal(t *testing.T, dryRun bool, client RecordsClient) {
	provider := newAzureProvider(
		NewDomainFilter([]string{""}),
		NewZoneIDFilter([]string{""}),
		dryRun,
		"group",
		&mockZonesClient{
			mockZoneListResult: &dns.ZoneListResult{
				Value: &[]dns.Zone{
					createMockZone("example.com", "/dnszones/example.com"),
					createMockZone("other.com", "/dnszones/other.com"),
				},
			},
		},
		client,
	)

	createRecords := []*endpoint.Endpoint{
		endpoint.NewEndpoint("example.com", "1.2.3.4", endpoint.RecordTypeA),
		endpoint.NewEndpoint("example.com", "tag", endpoint.RecordTypeTXT),
		endpoint.NewEndpoint("foo.example.com", "1.2.3.4", endpoint.RecordTypeA),
		endpoint.NewEndpoint("foo.example.com", "tag", endpoint.RecordTypeTXT),
		endpoint.NewEndpoint("bar.example.com", "other.com", endpoint.RecordTypeCNAME),
		endpoint.NewEndpoint("bar.example.com", "tag", endpoint.RecordTypeTXT),
		endpoint.NewEndpoint("other.com", "5.6.7.8", endpoint.RecordTypeA),
		endpoint.NewEndpoint("other.com", "tag", endpoint.RecordTypeTXT),
		endpoint.NewEndpoint("nope.com", "4.4.4.4", endpoint.RecordTypeA),
		endpoint.NewEndpoint("nope.com", "tag", endpoint.RecordTypeTXT),
	}

	currentRecords := []*endpoint.Endpoint{
		endpoint.NewEndpoint("old.example.com", "121.212.121.212", endpoint.RecordTypeA),
		endpoint.NewEndpoint("oldcname.example.com", "other.com", endpoint.RecordTypeCNAME),
		endpoint.NewEndpoint("old.nope.com", "121.212.121.212", endpoint.RecordTypeA),
	}
	updatedRecords := []*endpoint.Endpoint{
		endpoint.NewEndpointWithTTL("new.example.com", "111.222.111.222", endpoint.RecordTypeA, 3600),
		endpoint.NewEndpointWithTTL("newcname.example.com", "other.com", endpoint.RecordTypeCNAME, 10),
		endpoint.NewEndpoint("new.nope.com", "222.111.222.111", endpoint.RecordTypeA),
	}

	deleteRecords := []*endpoint.Endpoint{
		endpoint.NewEndpoint("deleted.example.com", "111.222.111.222", endpoint.RecordTypeA),
		endpoint.NewEndpoint("deletedcname.example.com", "other.com", endpoint.RecordTypeCNAME),
		endpoint.NewEndpoint("deleted.nope.com", "222.111.222.111", endpoint.RecordTypeA),
	}

	changes := &plan.Changes{
		Create:    createRecords,
		UpdateNew: updatedRecords,
		UpdateOld: currentRecords,
		Delete:    deleteRecords,
	}

	if err := provider.ApplyChanges(changes); err != nil {
		t.Fatal(err)
	}
}
