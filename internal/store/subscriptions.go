package store

import "fmt"

// Subscription is one (subscriber ← subscribee) delivery edge.
type Subscription struct {
	Subscriber string
	Subscribee string
	LastCount  int
}

// AddSubscription inserts an edge (idempotent: re-subscribing is a no-op).
func (s *Store) AddSubscription(subscriber, subscribee string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO subscriptions (subscriber, subscribee) VALUES (?, ?)`, subscriber, subscribee)
	return err
}

// RemoveSubscription deletes an edge. Removing a non-existent edge is a LOUD
// error (a typo must not silently "succeed").
func (s *Store) RemoveSubscription(subscriber, subscribee string) error {
	res, err := s.db.Exec(`DELETE FROM subscriptions WHERE subscriber = ? AND subscribee = ?`, subscriber, subscribee)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: no subscription %s -> %s", subscriber, subscribee)
	}
	return nil
}

// SubscribersOf returns the edges delivering subscribee's turns.
func (s *Store) SubscribersOf(subscribee string) ([]Subscription, error) {
	return s.querySubs(`SELECT subscriber, subscribee, last_count FROM subscriptions WHERE subscribee = ?`, subscribee)
}

// SubscriptionsOf returns every edge touching id (either side).
func (s *Store) SubscriptionsOf(id string) ([]Subscription, error) {
	return s.querySubs(`SELECT subscriber, subscribee, last_count FROM subscriptions WHERE subscriber = ? OR subscribee = ?`, id, id)
}

// AllSubscriptions returns every edge (tracker seeding at daemon start).
func (s *Store) AllSubscriptions() ([]Subscription, error) {
	return s.querySubs(`SELECT subscriber, subscribee, last_count FROM subscriptions`)
}

// SetSubscriptionCount persists an edge's last-delivered reply count.
func (s *Store) SetSubscriptionCount(subscriber, subscribee string, count int) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET last_count = ? WHERE subscriber = ? AND subscribee = ?`, count, subscriber, subscribee)
	return err
}

func (s *Store) querySubs(q string, args ...any) ([]Subscription, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.Subscriber, &sub.Subscribee, &sub.LastCount); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}
