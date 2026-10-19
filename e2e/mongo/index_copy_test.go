//go:build e2e

package mongo_e2e

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/stubbedev/treeman/e2e/harness"
	"github.com/stubbedev/treeman/internal/config"
	dbmongo "github.com/stubbedev/treeman/internal/db/mongo"
)

// The $out clone fallback rebuilt indexes from a whitelist matched by
// Go type: a partial unique index came back as a full unique one (the
// v2 driver decodes nested documents as bson.D, not bson.M), a TTL
// stored as a double lost its TTL, and collation / text weights were
// never copied. The template's index specs must equal the source's.
func TestOutCloneKeepsEveryIndexOption(t *testing.T) {
	harness.SkipIfNoDocker(t)
	t.Cleanup(harness.ComposeUp(t, harness.MustAbs(".")))
	waitForMongo(t)
	t.Setenv("PATH", t.TempDir()) // no mongodump/mongorestore: force the $out path

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(wireURI))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()
	src, tpl := client.Database("treeman_idx_src"), client.Database("treeman_idx_tpl")
	t.Cleanup(func() { _ = src.Drop(context.Background()); _ = tpl.Drop(context.Background()) })

	if _, err := src.Collection("users").InsertOne(ctx, bson.D{{Key: "email", Value: "a@b"}, {Key: "active", Value: true}}); err != nil {
		t.Fatal(err)
	}
	indexes := bson.A{
		bson.D{
			{Key: "key", Value: bson.D{{Key: "email", Value: 1}}},
			{Key: "name", Value: "email_active_unique"},
			{Key: "unique", Value: true},
			{Key: "partialFilterExpression", Value: bson.D{{Key: "active", Value: true}}},
		},
		bson.D{
			{Key: "key", Value: bson.D{{Key: "createdAt", Value: 1}}},
			{Key: "name", Value: "ttl"},
			{Key: "expireAfterSeconds", Value: 3600.0},
		},
		bson.D{
			{Key: "key", Value: bson.D{{Key: "name", Value: 1}}},
			{Key: "name", Value: "name_ci"},
			{Key: "collation", Value: bson.D{{Key: "locale", Value: "en"}, {Key: "strength", Value: 2}}},
		},
		bson.D{
			{Key: "key", Value: bson.D{{Key: "bio", Value: "text"}}},
			{Key: "name", Value: "bio_text"},
			{Key: "weights", Value: bson.D{{Key: "bio", Value: 5}}},
		},
	}
	if err := src.RunCommand(ctx, bson.D{{Key: "createIndexes", Value: "users"}, {Key: "indexes", Value: indexes}}).Err(); err != nil {
		t.Fatal(err)
	}

	drv, err := dbmongo.Connect(ctx, config.MongoConn{URI: wireURI})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = drv.Close(context.Background()) }()
	if err := drv.SnapshotCreate(ctx, src.Name(), tpl.Name()); err != nil {
		t.Fatalf("snapshot create: %v", err)
	}

	want, got := indexSpecs(ctx, t, src.Collection("users")), indexSpecs(ctx, t, tpl.Collection("users"))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("template indexes differ from source:\n got  %v\n want %v", got, want)
	}
}

// indexSpecs lists a collection's index specs keyed by name, without
// the per-collection `ns` field.
func indexSpecs(ctx context.Context, t *testing.T, c *mongo.Collection) map[string]bson.M {
	t.Helper()
	cur, err := c.Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var specs []bson.M
	if err := cur.All(ctx, &specs); err != nil {
		t.Fatal(err)
	}
	out := map[string]bson.M{}
	for _, s := range specs {
		delete(s, "ns")
		out[s["name"].(string)] = s
	}
	return out
}
