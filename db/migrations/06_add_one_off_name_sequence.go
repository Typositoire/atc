package migrations

import "github.com/concourse/atc/dbng/migration"

func AddOneOffNameSequence(tx migration.LimitedTx) error {
	_, err := tx.Exec(`CREATE SEQUENCE one_off_name START 1`)
	return err
}
