// Package mongostore implements shop.Store on MongoDB.
package mongostore

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"github.com/Amitreddy14/tremor/internal/shop"
)

// Store keeps orders in a MongoDB collection.
type Store struct {
	client *mongo.Client
	orders *mongo.Collection
}

type orderDoc struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"`
	Item      string             `bson:"item"`
	Quantity  int                `bson:"quantity"`
	CreatedAt time.Time          `bson:"created_at"`
}

// New creates a Store. The driver connects lazily, so New succeeds even while
// MongoDB is down; Ping reports availability. Short timeouts make requests fail
// fast during an outage instead of hanging.
func New(ctx context.Context, uri, database string) (*Store, error) {
	opts := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(2 * time.Second).
		SetConnectTimeout(2 * time.Second)
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Store{client: client, orders: client.Database(database).Collection("orders")}, nil
}

// Close disconnects from MongoDB.
func (s *Store) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

func (s *Store) Ping(ctx context.Context) error {
	return s.client.Ping(ctx, readpref.Primary())
}

func (s *Store) Create(ctx context.Context, o shop.Order) (shop.Order, error) {
	res, err := s.orders.InsertOne(ctx, orderDoc{Item: o.Item, Quantity: o.Quantity, CreatedAt: o.CreatedAt})
	if err != nil {
		return shop.Order{}, err
	}
	if id, ok := res.InsertedID.(primitive.ObjectID); ok {
		o.ID = id.Hex()
	}
	return o, nil
}

func (s *Store) List(ctx context.Context, limit int) ([]shop.Order, error) {
	cur, err := s.orders.Find(ctx, bson.D{},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var docs []orderDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]shop.Order, 0, len(docs))
	for _, d := range docs {
		out = append(out, shop.Order{ID: d.ID.Hex(), Item: d.Item, Quantity: d.Quantity, CreatedAt: d.CreatedAt})
	}
	return out, nil
}
