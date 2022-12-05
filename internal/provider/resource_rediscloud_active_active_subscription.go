package provider

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/RedisLabs/rediscloud-go-api/redis"
	"github.com/RedisLabs/rediscloud-go-api/service/subscriptions"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

func resourceRedisCloudActiveActiveSubscription() *schema.Resource {
	return &schema.Resource{
		Description:   "Creates a Subscription and database resources within your Redis Enterprise Cloud Account.",
		CreateContext: resourceRedisCloudActiveActiveSubscriptionCreate,
		ReadContext:   resourceRedisCloudActiveActiveSubscriptionRead,
		UpdateContext: resourceRedisCloudActiveActiveSubscriptionUpdate,
		DeleteContext: resourceRedisCloudActiveActiveSubscriptionDelete,
		CustomizeDiff: func(ctx context.Context, diff *schema.ResourceDiff, i interface{}) error {
			_, cPlanExists := diff.GetOk("creation_plan")
			if cPlanExists {
				return nil
			}

			// The resource hasn't been created yet, but the creation plan is missing.
			if diff.Id() == "" {
				return fmt.Errorf(`the "creation_plan" block is required`)
			}
			return nil
		},

		Importer: &schema.ResourceImporter{
			// Let the READ operation do the heavy lifting for importing values from the API.
			StateContext: schema.ImportStatePassthroughContext,
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(30 * time.Minute),
			Read:   schema.DefaultTimeout(10 * time.Minute),
			Update: schema.DefaultTimeout(30 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Description: "A meaningful name to identify the subscription",
				Type:        schema.TypeString,
				Optional:    true,
			},
			"payment_method": {
				Description:      "Payment method for the requested subscription. If credit card is specified, the payment method Id must be defined.",
				Type:             schema.TypeString,
				ForceNew:         true,
				ValidateDiagFunc: validateDiagFunc(validation.StringMatch(regexp.MustCompile("^(credit-card|marketplace)$"), "must be 'credit-card' or 'marketplace'")),
				Optional:         true,
				Default:          "credit-card",
			},
			"payment_method_id": {
				Computed:         true,
				Description:      "A valid payment method pre-defined in the current account",
				Type:             schema.TypeString,
				ValidateDiagFunc: validateDiagFunc(validation.StringMatch(regexp.MustCompile("^\\d+$"), "must be a number")),
				Optional:         true,
			},
			"cloud_provider": {
				Description:      "A cloud provider string either GCP or AWS",
				Type:             schema.TypeString,
				Required:         true,
				ForceNew:         true,
				ValidateDiagFunc: validateDiagFunc(validation.StringMatch(regexp.MustCompile("^(GCP|AWS)$"), "must be 'GCP' or 'AWS'")),
			},
			"creation_plan": {
				Description: "Information about the planned databases used to optimise the database infrastructure. This information is only used when creating a new subscription and any changes will be ignored after this.",
				Type:        schema.TypeList,
				MaxItems:    1,
				// The block is required when the user provisions a new subscription.
				// The block is ignored in the UPDATE operation or after IMPORTing the resource.
				// Custom validation is handled in CustomizeDiff.
				Optional: true,
				DiffSuppressFunc: func(k, old, new string, d *schema.ResourceData) bool {
					if d.Id() == "" {
						// We don't want to ignore the block if the resource is about to be created.
						return false
					}
					return true
				},
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"memory_limit_in_gb": {
							Description: "Maximum memory usage for each database",
							Type:        schema.TypeFloat,
							Required:    true,
						},
						"quantity": {
							Description:  "The planned number of databases",
							Type:         schema.TypeInt,
							Required:     true,
							ValidateFunc: validation.IntAtLeast(1),
						},
						"support_oss_cluster_api": {
							Description: "Support Redis open-source (OSS) Cluster API",
							Type:        schema.TypeBool,
							Required:    true,
						},
						"region": {
							// TODO: Add description for the region.
							Description: "",
							Type:        schema.TypeSet,
							Required:    true,
							MinItems:    1,
							Set: func(v interface{}) int {
								var buf bytes.Buffer
								m := v.(map[string]interface{})
								buf.WriteString(fmt.Sprintf("%s-", m["region"].(string)))
								return schema.HashString(buf.String())
							},
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"region": {
										Description: "Deployment region as defined by cloud provider",
										Type:        schema.TypeString,
										Required:    true,
									},
									"networking_deployment_cidr": {
										Description:      "Deployment CIDR mask",
										Type:             schema.TypeString,
										Required:         true,
										ValidateDiagFunc: validateDiagFunc(validation.IsCIDR),
									},
									"write_operations_per_second": {
										Description: "Write operations per second",
										Type:        schema.TypeInt,
										Required:    true,
									},
									"read_operations_per_second": {
										Description: "Write operations per second",
										Type:        schema.TypeInt,
										Required:    true,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func resourceRedisCloudActiveActiveSubscriptionCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	api := meta.(*apiClient)

	plan := d.Get("creation_plan").([]interface{})

	// Create creation-plan databases
	planMap := plan[0].(map[string]interface{})

	// Create CloudProviders
	providers, err := buildCreateActiveActiveCloudProviders(d.Get("cloud_provider").(string), planMap)
	if err != nil {
		return diag.FromErr(err)
	}

	// Create Subscription
	name := d.Get("name").(string)

	paymentMethod := d.Get("payment_method").(string)
	paymentMethodID, err := readPaymentMethodID(d)
	if err != nil {
		return diag.FromErr(err)
	}

	// Create databases
	var dbs []*subscriptions.CreateDatabase

	dbs = buildSubscriptionCreatePlanAADatabases(planMap)

	createSubscriptionRequest := subscriptions.CreateSubscription{
		DeploymentType:  redis.String("active-active"),
		Name:            redis.String(name),
		DryRun:          redis.Bool(false),
		PaymentMethodID: paymentMethodID,
		PaymentMethod:   redis.String(paymentMethod),
		CloudProviders:  providers,
		Databases:       dbs,
	}

	subId, err := api.client.Subscription.Create(ctx, createSubscriptionRequest)
	if err != nil {
		return diag.FromErr(err)
	}

	d.SetId(strconv.Itoa(subId))

	// Confirm Subscription Active status
	err = waitForSubscriptionToBeActive(ctx, subId, api)
	if err != nil {
		return diag.FromErr(err)
	}

	// There is a timing issue where the subscription is marked as active before the creation-plan databases are listed .
	// This additional wait ensures that the databases will be listed before calling api.client.Database.List()
	time.Sleep(10 * time.Second) //lintignore:R018
	if err := waitForSubscriptionToBeActive(ctx, subId, api); err != nil {
		return diag.FromErr(err)
	}

	// Locate Databases to confirm Active status
	dbList := api.client.Database.List(ctx, subId)

	for dbList.Next() {
		dbId := *dbList.Value().ID

		if err := waitForDatabaseToBeActive(ctx, subId, dbId, api); err != nil {
			return diag.FromErr(err)
		}
		// Delete each creation-plan database
		dbErr := api.client.Database.Delete(ctx, subId, dbId)
		if dbErr != nil {
			diag.FromErr(dbErr)
		}
	}
	if dbList.Err() != nil {
		return diag.FromErr(dbList.Err())
	}

	// Check that the subscription is in an active state before calling the read function
	if err := waitForSubscriptionToBeActive(ctx, subId, api); err != nil {
		return diag.FromErr(err)
	}

	// Some attributes on a database are not accessible by the subscription creation API.
	// Run the subscription update function to apply any additional changes to the databases, such as password and so on.
	// TODO: add this line back in when implemented
	// can this just be a read?
	//return resourceRedisCloudSubscriptionUpdate(ctx, d, meta)
	return resourceRedisCloudActiveActiveSubscriptionRead(ctx, d, meta)
}

func resourceRedisCloudActiveActiveSubscriptionRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	api := meta.(*apiClient)

	var diags diag.Diagnostics

	subId, err := strconv.Atoi(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	subscription, err := api.client.Subscription.Get(ctx, subId)
	if err != nil {
		if _, ok := err.(*subscriptions.NotFound); ok {
			d.SetId("")
			return diags
		}
		return diag.FromErr(err)
	}

	if err := d.Set("name", redis.StringValue(subscription.Name)); err != nil {
		return diag.FromErr(err)
	}

	if subscription.PaymentMethodID != nil && redis.IntValue(subscription.PaymentMethodID) != 0 {
		paymentMethodID := strconv.Itoa(redis.IntValue(subscription.PaymentMethodID))
		if err := d.Set("payment_method_id", paymentMethodID); err != nil {
			return diag.FromErr(err)
		}
	}
	if err := d.Set("payment_method", redis.StringValue(subscription.PaymentMethod)); err != nil {
		return diag.FromErr(err)
	}

	// TODO: write a better error message
	cloudDetails := subscription.CloudDetails
	if len(cloudDetails) == 0 {
		return diag.FromErr(fmt.Errorf("Cloud details is empty. Subscription status: %s", redis.StringValue(subscription.Status)))
	}
	cloudProvider := cloudDetails[0].Provider
	if err := d.Set("cloud_provider", cloudProvider); err != nil {
		return diag.FromErr(err)
	}

	return diags
}

func resourceRedisCloudActiveActiveSubscriptionUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	api := meta.(*apiClient)

	subId, err := strconv.Atoi(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	subscriptionMutex.Lock(subId)
	defer subscriptionMutex.Unlock(subId)

	if d.HasChanges("name", "payment_method_id") {
		updateSubscriptionRequest := subscriptions.UpdateSubscription{}

		if d.HasChange("name") {
			name := d.Get("name").(string)
			updateSubscriptionRequest.Name = &name
		}

		if d.HasChange("payment_method_id") {
			paymentMethodID, err := readPaymentMethodID(d)
			if err != nil {
				return diag.FromErr(err)
			}

			updateSubscriptionRequest.PaymentMethodID = paymentMethodID
		}

		err = api.client.Subscription.Update(ctx, subId, updateSubscriptionRequest)
		if err != nil {
			return diag.FromErr(err)
		}
	}

	if err := waitForSubscriptionToBeActive(ctx, subId, api); err != nil {
		return diag.FromErr(err)
	}

	return resourceRedisCloudSubscriptionRead(ctx, d, meta)
}

func resourceRedisCloudActiveActiveSubscriptionDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	// use the meta value to retrieve your client from the provider configure method
	api := meta.(*apiClient)

	var diags diag.Diagnostics

	subId, err := strconv.Atoi(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	subscriptionMutex.Lock(subId)
	defer subscriptionMutex.Unlock(subId)

	// Wait for the subscription to be active before deleting it.
	if err := waitForSubscriptionToBeActive(ctx, subId, api); err != nil {
		return diag.FromErr(err)
	}

	// There is a timing issue where the subscription is marked as active before the creation-plan databases are deleted.
	// This additional wait ensures that the databases are deleted before the subscription is deleted.
	time.Sleep(10 * time.Second) //lintignore:R018
	if err := waitForSubscriptionToBeActive(ctx, subId, api); err != nil {
		return diag.FromErr(err)
	}
	// Delete subscription once all databases are deleted
	err = api.client.Subscription.Delete(ctx, subId)
	if err != nil {
		return diag.FromErr(err)
	}

	d.SetId("")

	err = waitForSubscriptionToBeDeleted(ctx, subId, api)
	if err != nil {
		return diag.FromErr(err)
	}

	return diags
}

func buildCreateActiveActiveCloudProviders(provider string, creationPlan map[string]interface{}) ([]*subscriptions.CreateCloudProvider, error) {

	createRegions := make([]*subscriptions.CreateRegion, 0)
	if regions := creationPlan["region"].(*schema.Set).List(); len(regions) != 0 {

		for _, region := range regions {
			regionMap := region.(map[string]interface{})

			regionStr := regionMap["region"].(string)

			createRegion := subscriptions.CreateRegion{
				Region: redis.String(regionStr),
				// TODO: Need to see what the API defaults to
				// MultipleAvailabilityZones:  redis.Bool(multipleAvailabilityZones),
				// PreferredAvailabilityZones: interfaceToStringSlice(preferredAZs),
			}

			if v, ok := regionMap["networking_deployment_cidr"]; ok && v != "" {
				createRegion.Networking = &subscriptions.CreateNetworking{
					DeploymentCIDR: redis.String(v.(string)),
				}
			}

			if v, ok := regionMap["networking_vpc_id"]; ok && v != "" {
				if createRegion.Networking == nil {
					createRegion.Networking = &subscriptions.CreateNetworking{}
				}
				createRegion.Networking.VPCId = redis.String(v.(string))
			}

			createRegions = append(createRegions, &createRegion)
		}
	}

	createCloudProviders := make([]*subscriptions.CreateCloudProvider, 0)
	createCloudProvider := &subscriptions.CreateCloudProvider{
		Provider:       redis.String(provider),
		CloudAccountID: redis.Int(1), // Active-Active subscriptions are created with Redis internal resources
		Regions:        createRegions,
	}

	createCloudProviders = append(createCloudProviders, createCloudProvider)

	return createCloudProviders, nil
}

func buildSubscriptionCreatePlanAADatabases(planMap map[string]interface{}) []*subscriptions.CreateDatabase {

	createDatabases := make([]*subscriptions.CreateDatabase, 0)

	dbName := "creation-plan-db-"
	idx := 1
	// throughputMeasurementBy := planMap["throughput_measurement_by"].(string)
	// throughputMeasurementValue := planMap["throughput_measurement_value"].(int)
	// averageItemSizeInBytes := planMap["average_item_size_in_bytes"].(int)
	numDatabases := planMap["quantity"].(int)
	supportOSSClusterAPI := planMap["support_oss_cluster_api"].(bool)
	regions := planMap["region"]
	var localThroughputs []*subscriptions.CreateLocalThroughput
	for _, v := range regions.(*schema.Set).List() {
		region := v.(map[string]interface{})
		localThroughputs = append(localThroughputs, &subscriptions.CreateLocalThroughput{
			Region:                   redis.String(region["region"].(string)),
			WriteOperationsPerSecond: redis.Int(region["write_operations_per_second"].(int)),
			ReadOperationsPerSecond:  redis.Int(region["read_operations_per_second"].(int)),
		})
	}
	// create the remaining DBs with all other modules
	createDatabases = append(createDatabases, createAADatabase(dbName, &idx, supportOSSClusterAPI, localThroughputs, numDatabases)...)

	return createDatabases
}

// createDatabase returns a CreateDatabase struct with the given parameters
func createAADatabase(dbName string, idx *int, supportOSSClusterAPI bool, localThroughputs []*subscriptions.CreateLocalThroughput, numDatabases int) []*subscriptions.CreateDatabase {

	var databases []*subscriptions.CreateDatabase
	for i := 0; i < numDatabases; i++ {
		createDatabase := subscriptions.CreateDatabase{
			Name:                       redis.String(dbName + strconv.Itoa(*idx)),
			Protocol:                   redis.String("redis"),
			SupportOSSClusterAPI:       redis.Bool(supportOSSClusterAPI),
			MemoryLimitInGB:            redis.Float64(1000),
			LocalThroughputMeasurement: localThroughputs,
			Quantity:                   redis.Int(1),
		}
		*idx++
		databases = append(databases, &createDatabase)
	}
	return databases
}

// func buildSubscriptionCreatePlanAADatabases(planMap map[string]interface{}) []*subscriptions.CreateDatabase {

// 	createDatabases := make([]*subscriptions.CreateDatabase, 0)

// 	dbName := "creation-plan-db-"
// 	idx := 1
// 	memoryLimitInGB := planMap["memory_limit_in_gb"].(float64)
// 	throughputMeasurementBy := planMap["throughput_measurement_by"].(string)
// 	throughputMeasurementValue := planMap["throughput_measurement_value"].(int)
// 	averageItemSizeInBytes := planMap["average_item_size_in_bytes"].(int)
// 	numDatabases := planMap["quantity"].(int)
// 	supportOSSClusterAPI := planMap["support_oss_cluster_api"].(bool)
// 	replication := planMap["replication"].(bool)
// 	planModules := interfaceToStringSlice(planMap["modules"].([]interface{}))

// 	// Check if one of the modules is RedisGraph
// 	containsGraph := false
// 	for _, module := range planModules {
// 		if *module == "RedisGraph" {
// 			containsGraph = true
// 			break
// 		}
// 	}

// 	if !containsGraph || len(planModules) <= 1 {
// 		var modules []*subscriptions.CreateModules
// 		for _, v := range planModules {
// 			modules = append(modules, &subscriptions.CreateModules{Name: v})
// 		}
// 		createDatabases = append(createDatabases, createDatabase(dbName, &idx, modules, throughputMeasurementBy, throughputMeasurementValue, memoryLimitInGB, averageItemSizeInBytes, supportOSSClusterAPI, replication, numDatabases)...)
// 	} else {
// 		// make RedisGraph module the first module, then append the rest of the modules
// 		var modules []*subscriptions.CreateModules
// 		modules = append(modules, &subscriptions.CreateModules{Name: redis.String("RedisGraph")})
// 		for _, v := range planModules {
// 			if *v != "RedisGraph" {
// 				modules = append(modules, &subscriptions.CreateModules{Name: v})
// 			}
// 		}
// 		// create a DB with the RedisGraph module
// 		createDatabases = append(createDatabases, createDatabase(dbName, &idx, modules[:1], throughputMeasurementBy, throughputMeasurementValue, memoryLimitInGB, averageItemSizeInBytes, supportOSSClusterAPI, replication, 1)...)
// 		if numDatabases == 1 {
// 			// create one extra DB with all other modules
// 			createDatabases = append(createDatabases, createDatabase(dbName, &idx, modules[1:], throughputMeasurementBy, throughputMeasurementValue, memoryLimitInGB, averageItemSizeInBytes, supportOSSClusterAPI, replication, 1)...)
// 		} else if numDatabases > 1 {
// 			// create the remaining DBs with all other modules
// 			createDatabases = append(createDatabases, createDatabase(dbName, &idx, modules[1:], throughputMeasurementBy, throughputMeasurementValue, memoryLimitInGB, averageItemSizeInBytes, supportOSSClusterAPI, replication, numDatabases-1)...)
// 		}
// 	}
// 	return createDatabases
// }

// createDatabase returns a CreateDatabase struct with the given parameters
// func createDatabase(dbName string, idx *int, modules []*subscriptions.CreateModules, throughputMeasurementBy string, throughputMeasurementValue int, memoryLimitInGB float64, averageItemSizeInBytes int, supportOSSClusterAPI bool, replication bool, numDatabases int) []*subscriptions.CreateDatabase {
// 	createThroughput := &subscriptions.CreateThroughput{
// 		By:    redis.String(throughputMeasurementBy),
// 		Value: redis.Int(throughputMeasurementValue),
// 	}
// 	if len(modules) > 0 {
// 		// if RediSearch is in the modules, then set the throughput to number-of-shards and convert the value
// 		search := false
// 		for _, module := range modules {
// 			if *module.Name == "RediSearch" {
// 				search = true
// 				break
// 			}
// 		}
// 		if search {
// 			if *createThroughput.By == "operations-per-second" {
// 				createThroughput.By = redis.String("number-of-shards")
// 				if replication {
// 					createThroughput.Value = redis.Int(*createThroughput.Value / 500)
// 				} else {
// 					createThroughput.Value = redis.Int(*createThroughput.Value / 1000)
// 				}
// 			}
// 		}
// 		// if RedisGraph is in the modules, set throughput to operations-per-second and convert the value
// 		if *modules[0].Name == "RedisGraph" {
// 			if *createThroughput.By == "number-of-shards" {
// 				createThroughput.By = redis.String("operations-per-second")
// 				if replication {
// 					createThroughput.Value = redis.Int(*createThroughput.Value * 500)
// 				} else {
// 					createThroughput.Value = redis.Int(*createThroughput.Value * 250)
// 				}
// 			}
// 		}
// 	}
// 	var databases []*subscriptions.CreateDatabase
// 	for i := 0; i < numDatabases; i++ {
// 		createDatabase := subscriptions.CreateDatabase{
// 			Name:                   redis.String(dbName + strconv.Itoa(*idx)),
// 			Protocol:               redis.String("redis"),
// 			MemoryLimitInGB:        redis.Float64(memoryLimitInGB),
// 			SupportOSSClusterAPI:   redis.Bool(supportOSSClusterAPI),
// 			Replication:            redis.Bool(replication),
// 			AverageItemSizeInBytes: redis.Int(averageItemSizeInBytes),
// 			ThroughputMeasurement:  createThroughput,
// 			Quantity:               redis.Int(1),
// 			Modules:                modules,
// 		}
// 		*idx++
// 		databases = append(databases, &createDatabase)
// 	}
// 	return databases
// }
