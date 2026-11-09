// Package mongostore implements runstore.Store on MongoDB.
package mongostore

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"github.com/Amitreddy14/tremor/internal/runstore"
)

const (
	// DefaultDatabase is the database that holds Tremor's history.
	DefaultDatabase = "tremor"
	collectionName  = "runs"
)

// Store keeps runs in the "runs" collection.
type Store struct {
	client *mongo.Client
	runs   *mongo.Collection
}

var _ runstore.Store = (*Store)(nil)

// New connects to MongoDB. The driver connects lazily, so New succeeds while the
// database is down; use Ping to check. Short timeouts keep a dead database from
// stalling the operator.
func New(ctx context.Context, uri, database string) (*Store, error) {
	opts := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(5 * time.Second).
		SetConnectTimeout(5 * time.Second)
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Store{client: client, runs: client.Database(database).Collection(collectionName)}, nil
}

// Close disconnects from MongoDB.
func (s *Store) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

// Ping checks that the primary is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.client.Ping(ctx, readpref.Primary())
}

// EnsureIndexes creates the indexes history queries rely on. It is idempotent.
func (s *Store) EnsureIndexes(ctx context.Context) error {
	_, err := s.runs.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "started_at", Value: -1}}},
		{Keys: bson.D{{Key: "namespace", Value: 1}, {Key: "name", Value: 1}, {Key: "started_at", Value: -1}}},
	})
	return err
}

// Save inserts or replaces the run with the same ID.
func (s *Store) Save(ctx context.Context, r runstore.Run) error {
	_, err := s.runs.ReplaceOne(ctx, bson.D{{Key: "_id", Value: r.ID}}, r, options.Replace().SetUpsert(true))
	return err
}

// List returns matching runs, newest first.
func (s *Store) List(ctx context.Context, f runstore.Filter) ([]runstore.Run, error) {
	filter := bson.D{}
	add := func(key, val string) {
		if val != "" {
			filter = append(filter, bson.E{Key: key, Value: val})
		}
	}
	add("namespace", f.Namespace)
	add("name", f.Name)
	add("fault_type", f.FaultType)
	add("result", f.Result)
	if !f.Since.IsZero() {
		filter = append(filter, bson.E{Key: "started_at", Value: bson.D{{Key: "$gte", Value: f.Since}}})
	}

	opts := options.Find().SetSort(bson.D{{Key: "started_at", Value: -1}})
	if f.Limit > 0 {
		opts.SetLimit(int64(f.Limit))
	}
	cur, err := s.runs.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var out []runstore.Run
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns the run with the given ID, or runstore.ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (runstore.Run, error) {
	var r runstore.Run
	err := s.runs.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&r)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return runstore.Run{}, runstore.ErrNotFound
	}
	return r, err
}
