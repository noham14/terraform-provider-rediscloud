package provider

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
)

func TestAccResourceRedisCloudActiveActiveSubscriptionPeering_aws(t *testing.T) {

	name := acctest.RandomWithPrefix(testResourcePrefix)

	// testCloudAccountName := os.Getenv("AWS_TEST_CLOUD_ACCOUNT_NAME")
	os.Setenv("AWS_VPC_CIDR", "10.0.0.0/24")

	cidrRange := "10.0.0.0/24"
	// Chose a CIDR range for the subscription that's unlikely to overlap with any VPC CIDR
	subCidrRange := "101.0.10.0/24"

	overlap, err := cidrRangesOverlap(subCidrRange, cidrRange)
	if err != nil {
		t.Fatalf("AWS_VPC_CIDR is not a valid CIDR range %s: %s", cidrRange, err)
	}
	if overlap {
		subCidrRange = "172.16.0.0/24"
	}
	os.Setenv("AWS_PEERING_REGION", "eu-west-2")
	os.Setenv("AWS_ACCOUNT_ID", "277885626557")
	os.Setenv("AWS_VPC_ID", "vpc-0896d84b605a91d75")

	peeringRegion := "eu-west-2"
	matchesRegex(t, peeringRegion, "^[a-z]+-[a-z]+-\\d+$")

	accountId := "277885626557"
	matchesRegex(t, accountId, "^\\d+$")

	vpcId := "vpc-0896d84b605a91d75"
	matchesRegex(t, vpcId, "^vpc-[a-z\\d]+$")

	tf := fmt.Sprintf(testAccResourceRedisCloudActiveActiveSubscriptionPeeringAWS,
		name,
		subCidrRange,
		peeringRegion,
		accountId,
		vpcId,
		cidrRange,
	)
	resourceName := "rediscloud_subscription_peering.test"

	resource.ParallelTest(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t); testAccAwsPeeringPreCheck(t); testAccAwsPreExistingCloudAccountPreCheck(t) },
		ProviderFactories: providerFactories,
		CheckDestroy:      testAccCheckSubscriptionDestroy,
		Steps: []resource.TestStep{
			{
				Config: tf,
				Check: resource.ComposeTestCheckFunc(
					resource.TestMatchResourceAttr(resourceName, "id", regexp.MustCompile("^\\d*/\\d*$")),
					resource.TestCheckResourceAttrSet(resourceName, "status"),
					resource.TestCheckResourceAttrSet(resourceName, "provider_name"),
					resource.TestCheckResourceAttrSet(resourceName, "aws_account_id"),
					resource.TestCheckResourceAttrSet(resourceName, "vpc_id"),
					resource.TestCheckResourceAttrSet(resourceName, "vpc_cidr"),
					resource.TestCheckResourceAttrSet(resourceName, "region"),
					resource.TestCheckResourceAttrSet(resourceName, "aws_peering_id"),
				),
			},
		},
	})
}

func TestAccResourceRedisCloudActiveActiveSubscriptionPeering_gcp(t *testing.T) {

	if testing.Short() {
		t.Skip("Required environment variables currently not available under CI")
	}

	name := acctest.RandomWithPrefix(testResourcePrefix)

	tf := fmt.Sprintf(testAccResourceRedisCloudSubscriptionPeeringGCP,
		name,
		os.Getenv("GCP_VPC_PROJECT"),
		os.Getenv("GCP_VPC_ID"),
	)
	resourceName := "rediscloud_subscription_peering.test"

	resource.ParallelTest(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: providerFactories,
		CheckDestroy:      testAccCheckSubscriptionDestroy,
		Steps: []resource.TestStep{
			{
				Config: tf,
				Check: resource.ComposeTestCheckFunc(
					resource.TestMatchResourceAttr(resourceName, "id", regexp.MustCompile("^\\d*/\\d*$")),
					resource.TestCheckResourceAttr(resourceName, "provider_name", "GCP"),
					resource.TestCheckResourceAttrSet(resourceName, "status"),
					resource.TestCheckResourceAttrSet(resourceName, "gcp_project_id"),
					resource.TestCheckResourceAttrSet(resourceName, "gcp_network_name"),
					resource.TestCheckResourceAttrSet(resourceName, "gcp_redis_project_id"),
					resource.TestCheckResourceAttrSet(resourceName, "gcp_redis_network_name"),
					resource.TestCheckResourceAttrSet(resourceName, "gcp_peering_id"),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func matchesRegexActiveActive(t *testing.T, value string, regex string) {
	if !regexp.MustCompile(regex).MatchString(value) {
		t.Fatalf("%s doesn't match regex %s", value, regex)
	}
}

func cidrRangesOverlapActiveActive(cidr1 string, cidr2 string) (bool, error) {
	_, first, err := net.ParseCIDR(cidr1)
	if err != nil {
		return false, err
	}
	_, second, err := net.ParseCIDR(cidr2)
	if err != nil {
		return false, err
	}

	overlaps := first.Contains(second.IP) || second.Contains(first.IP)

	return overlaps, nil
}

const testAccResourceRedisCloudActiveActiveSubscriptionPeeringAWS = `
data "rediscloud_payment_method" "card" {
  card_type = "Visa"
}

resource "rediscloud_active_active_subscription" "example" {
  name = "%s"
  payment_method_id = data.rediscloud_payment_method.card.id
  memory_storage = "ram"

  cloud_provider {
    provider = "AWS"
    region {
      region = "eu-west-1"
      networking_deployment_cidr = "%s"
      preferred_availability_zones = ["eu-west-1a"]
    }
  }

  creation_plan {
    average_item_size_in_bytes = 1
    memory_limit_in_gb = 1
    quantity = 1
    replication=false
    support_oss_cluster_api=false
    throughput_measurement_by = "operations-per-second"
    throughput_measurement_value = 10000
	modules = []
  }
}

resource "rediscloud_active_active_subscription_peering" "test" {
  subscription_id = rediscloud_subscription.example.id
  provider_name = "AWS"
  region = "%s"
  aws_account_id = "%s"
  vpc_id = "%s"
  vpc_cidr = "%s"
}
`

const testAccResourceRedisCloudActiveActiveSubscriptionPeeringGCP = `
data "rediscloud_payment_method" "card" {
  card_type = "Visa"
}

resource "rediscloud_subscription" "example" {
  name = "%s"
  payment_method_id = data.rediscloud_payment_method.card.id
  memory_storage = "ram"

  cloud_provider {
    provider = "GCP"
    cloud_account_id = 1
    region {
      region = "europe-west1"
      networking_deployment_cidr = "192.168.0.0/24"
      preferred_availability_zones = []
    }
  }

  creation_plan {
    average_item_size_in_bytes = 1
    memory_limit_in_gb = 1
    quantity = 1
    replication=false
    support_oss_cluster_api=false
    throughput_measurement_by = "operations-per-second"
    throughput_measurement_value = 10000
	modules = []
  }
}

resource "rediscloud_subscription_peering" "test" {
  subscription_id = rediscloud_subscription.example.id
  provider_name = "GCP"
  gcp_project_id = "%s"
  gcp_network_name = "%s"
}
`