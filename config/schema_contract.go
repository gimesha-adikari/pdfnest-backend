package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"gorm.io/gorm"
)

// Historical contract captured from master 296838852195f96ca5590b3466a33ec5ae3e39d9
// on PostgreSQL 18.6. It is intentionally independent of subsequent Go models.
// Updates require an explicit schema-version/compatibility review.
//
//go:embed managed_schema_contract.json
var historicalSchemaJSON []byte

type schemaColumn struct {
	Table       string  `json:"table"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	NotNull     bool    `json:"not_null"`
	DefaultExpr *string `json:"default_expr"`
	Identity    string  `json:"-"`
	Generated   string  `json:"-"`
	NonBuiltin  bool    `json:"-"`
}
type schemaIndex struct {
	Table      string `json:"table"`
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Unique     bool   `json:"unique"`
	Primary    bool   `json:"primary"`
	Valid      bool   `json:"-"`
	Ready      bool   `json:"-"`
	Live       bool   `json:"-"`
	KeyName    string `json:"-"`
	SafeExtra  bool   `json:"-"`
}
type schemaForeignKey struct {
	Table      string `json:"table"`
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Validated  bool   `json:"-"`
	Enforced   bool   `json:"-"`
	Schema     string `json:"-"`
	RefSchema  string `json:"-"`
	RefTable   string `json:"-"`
}
type schemaSequence struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Start     int64   `json:"start"`
	Increment int64   `json:"increment"`
	Min       int64   `json:"min"`
	Max       int64   `json:"max"`
	Cache     int64   `json:"cache"`
	Cycle     bool    `json:"cycle"`
	OwnedBy   *string `json:"owned_by"`
}
type schemaContract struct {
	Version     string             `json:"version"`
	Columns     []schemaColumn     `json:"columns"`
	Indexes     []schemaIndex      `json:"indexes"`
	ForeignKeys []schemaForeignKey `json:"foreign_keys"`
	Sequences   []schemaSequence   `json:"sequences"`
}

var loadHistoricalContract = sync.OnceValues(func() (schemaContract, error) {
	var c schemaContract
	if err := json.Unmarshal(historicalSchemaJSON, &c); err != nil {
		return c, fmt.Errorf("decode embedded schema contract: %w", err)
	}
	if c.Version != managedSchemaVersion || len(c.Columns) == 0 {
		return c, fmt.Errorf("invalid embedded historical schema contract")
	}
	return c, nil
})

// All catalog rendering uses pg_catalog search_path so qualified definitions and
// PostgreSQL type aliases are stable. No application rows or sequence counters
// are read. Extra objects are allowed, but cannot replace required definitions.
func validateHistoricalContract(tx *gorm.DB) error {
	expected, err := loadHistoricalContract()
	if err != nil {
		return err
	}
	if err = tx.Exec("SELECT pg_catalog.set_config('search_path', 'pg_catalog', true)").Error; err != nil {
		return fmt.Errorf("set schema inspection namespace: %w", err)
	}
	var columns []schemaColumn
	if err = tx.Raw(`SELECT c.relname AS "table",a.attname AS name,
 pg_catalog.format_type(a.atttypid,a.atttypmod) AS type,a.attnotnull AS not_null,a.attidentity AS identity,a.attgenerated AS generated,
 pg_catalog.pg_get_expr(d.adbin,d.adrelid) AS default_expr,
 (ty.typtype<>'b' OR tn.nspname<>'pg_catalog') AS non_builtin
 FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_catalog.pg_type ty ON ty.oid=a.atttypid JOIN pg_catalog.pg_namespace tn ON tn.oid=ty.typnamespace
 LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
 WHERE n.nspname='public' AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped`).Scan(&columns).Error; err != nil {
		return fmt.Errorf("inspect schema columns: %w", err)
	}
	actualColumns := make(map[string]schemaColumn, len(columns))
	for _, c := range columns {
		actualColumns[c.Table+"."+c.Name] = c
	}
	for _, c := range expected.Columns {
		if got, ok := actualColumns[c.Table+"."+c.Name]; !ok || !reflect.DeepEqual(got, c) {
			return fmt.Errorf("schema incompatible: required column %s.%s is missing or differs", c.Table, c.Name)
		}
	}
	// Additional columns may not introduce new required writes/default code.
	requiredTables := make(map[string]bool)
	for _, c := range expected.Columns {
		requiredTables[c.Table] = true
		delete(actualColumns, c.Table+"."+c.Name)
	}
	for _, c := range actualColumns {
		if requiredTables[c.Table] && (c.NotNull || c.DefaultExpr != nil || c.Identity != "" || c.Generated != "" || c.NonBuiltin) {
			return fmt.Errorf("schema incompatible: additional write-affecting column on %s", c.Table)
		}
	}
	var restrictedTables []string
	if err = tx.Raw(`SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relkind='r' AND (c.relpersistence<>'p' OR c.relrowsecurity OR c.relforcerowsecurity OR
 EXISTS(SELECT 1 FROM pg_catalog.pg_constraint k WHERE k.conrelid=c.oid AND k.contype IN ('c','u','x')) OR
 (c.relname<>'schema_migrations' AND EXISTS(SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid=c.oid AND NOT t.tgisinternal AND t.tgenabled<>'D')) OR
 (c.relname<>'schema_migrations' AND EXISTS(SELECT 1 FROM pg_catalog.pg_rewrite r WHERE r.ev_class=c.oid AND r.ev_enabled<>'D')))`).Scan(&restrictedTables).Error; err != nil {
		return fmt.Errorf("inspect schema enforcement: %w", err)
	}
	for _, table := range restrictedTables {
		if requiredTables[table] {
			return fmt.Errorf("schema incompatible: additional restrictions or durability flags on %s", table)
		}
	}
	// The applied ledger is SELECT-only; its user write triggers cannot affect
	// historical application CRUD. All required application tables reject them.
	var indexes []schemaIndex
	if err = tx.Raw(`SELECT t.relname AS "table",i.relname AS name,
 pg_catalog.pg_get_indexdef(i.oid) AS definition,x.indisunique AS "unique",
 x.indisprimary AS "primary",x.indisvalid AS valid,x.indisready AS ready,x.indislive AS live,
 coalesce(a.attname,'') AS key_name,
 coalesce((x.indnatts=x.indnkeyatts AND x.indnkeyatts=1 AND
 cardinality(x.indkey)=1 AND cardinality(x.indclass)=1 AND cardinality(x.indcollation)=1 AND cardinality(x.indoption)=1 AND
 x.indexprs IS NULL AND x.indpred IS NULL AND NOT x.indcheckxmin AND NOT x.indisexclusion AND
 i.relkind='i' AND i.reloptions IS NULL AND i.reltablespace=0 AND am.amname='btree' AND
 a.attnum>0 AND NOT a.attisdropped AND tn.nspname='pg_catalog' AND ty.typtype='b' AND
 (ty.typname IN ('int8','uuid') OR
 (ty.typname='varchar' AND a.atttypmod BETWEEN 5 AND 259 AND
 pg_catalog.current_setting('server_encoding')='UTF8' AND pg_catalog.current_setting('block_size')::integer>=8192)) AND
 x.indoption[0]=0 AND x.indcollation[0]=a.attcollation AND
 (a.attcollation=0 OR a.attcollation='pg_catalog."default"'::pg_catalog.regcollation) AND
 ons.nspname='pg_catalog' AND op.opcdefault AND op.opcmethod=i.relam AND
 ia.attnum=1 AND NOT ia.attisdropped AND ia.attoptions IS NULL),false) AS safe_extra
 FROM pg_catalog.pg_index x JOIN pg_catalog.pg_class i ON i.oid=x.indexrelid
 LEFT JOIN pg_catalog.pg_am am ON am.oid=i.relam
 JOIN pg_catalog.pg_class t ON t.oid=x.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace
 LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=t.oid AND a.attnum=x.indkey[0]
 LEFT JOIN pg_catalog.pg_type ty ON ty.oid=a.atttypid LEFT JOIN pg_catalog.pg_namespace tn ON tn.oid=ty.typnamespace
 LEFT JOIN pg_catalog.pg_opclass op ON op.oid=x.indclass[0] LEFT JOIN pg_catalog.pg_namespace ons ON ons.oid=op.opcnamespace
 LEFT JOIN pg_catalog.pg_attribute ia ON ia.attrelid=i.oid AND ia.attnum=1
 WHERE n.nspname='public'`).Scan(&indexes).Error; err != nil {
		return fmt.Errorf("inspect schema indexes: %w", err)
	}
	actualIndexes := make(map[string]schemaIndex, len(indexes))
	for _, i := range indexes {
		actualIndexes[i.Table+"."+i.Name] = i
	}
	for _, i := range expected.Indexes {
		got, ok := actualIndexes[i.Table+"."+i.Name]
		if !ok || !got.Valid || !got.Ready || !got.Live || got.Definition != i.Definition || got.Unique != i.Unique || got.Primary != i.Primary {
			return fmt.Errorf("schema incompatible: required index %s is missing or differs", i.Name)
		}
	}
	for _, i := range expected.Indexes {
		delete(actualIndexes, i.Table+"."+i.Name)
	}
	// Extras are deliberately single bounded historical keys, not arbitrary
	// nonunique btrees: even a text key can make otherwise valid writes fail.
	historicalKeys := make(map[string]bool, len(expected.Columns))
	for _, c := range expected.Columns {
		historicalKeys[c.Table+"."+c.Name] = true
	}
	for _, i := range actualIndexes {
		if requiredTables[i.Table] && (i.Unique || i.Primary || !i.Valid || !i.Ready || !i.Live || !i.SafeExtra || !historicalKeys[i.Table+"."+i.KeyName]) {
			return fmt.Errorf("schema incompatible: additional write-affecting index on %s", i.Table)
		}
	}
	var fks []schemaForeignKey
	if err = tx.Raw(`SELECT t.relname AS "table",c.conname AS name,
 pg_catalog.pg_get_constraintdef(c.oid) AS definition,c.convalidated AS validated,n.nspname AS schema,rn.nspname AS ref_schema,rt.relname AS ref_table,
 (SELECT count(*)=4 AND coalesce(bool_and(t.tgenabled IN ('O','A')),false) FROM pg_catalog.pg_trigger t WHERE t.tgconstraint=c.oid) AS enforced
 FROM pg_catalog.pg_constraint c JOIN pg_catalog.pg_class t ON t.oid=c.conrelid
 JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace
 JOIN pg_catalog.pg_class rt ON rt.oid=c.confrelid JOIN pg_catalog.pg_namespace rn ON rn.oid=rt.relnamespace
 WHERE c.contype='f'`).Scan(&fks).Error; err != nil {
		return fmt.Errorf("inspect schema foreign keys: %w", err)
	}
	actualFKs := make(map[string]schemaForeignKey, len(fks))
	for _, f := range fks {
		actualFKs[f.Schema+"."+f.Table+"."+f.Name] = f
	}
	for _, f := range expected.ForeignKeys {
		got, ok := actualFKs["public."+f.Table+"."+f.Name]
		if !ok || !got.Validated || !got.Enforced || got.Definition != f.Definition {
			return fmt.Errorf("schema incompatible: required foreign key %s is missing or differs", f.Name)
		}
	}
	for _, f := range expected.ForeignKeys {
		delete(actualFKs, "public."+f.Table+"."+f.Name)
	}
	for _, f := range actualFKs {
		if (f.Schema == "public" && requiredTables[f.Table]) || (f.RefSchema == "public" && requiredTables[f.RefTable]) {
			return fmt.Errorf("schema incompatible: additional foreign key on %s", f.Table)
		}
	}
	var sequences []schemaSequence
	if err = tx.Raw(`SELECT c.relname AS name,pg_catalog.format_type(s.seqtypid,NULL) AS type,
 s.seqstart AS start,s.seqincrement AS increment,s.seqmin AS min,s.seqmax AS max,s.seqcache AS cache,s.seqcycle AS cycle,
 (SELECT pg_catalog.format('%I.%I.%I',n2.nspname,c2.relname,a.attname)
 FROM pg_catalog.pg_depend d JOIN pg_catalog.pg_class c2 ON c2.oid=d.refobjid
 JOIN pg_catalog.pg_namespace n2 ON n2.oid=c2.relnamespace
 JOIN pg_catalog.pg_attribute a ON a.attrelid=c2.oid AND a.attnum=d.refobjsubid
 WHERE d.classid='pg_catalog.pg_class'::pg_catalog.regclass AND d.objid=c.oid AND d.deptype IN ('a','i')) AS owned_by
 FROM pg_catalog.pg_sequence s JOIN pg_catalog.pg_class c ON c.oid=s.seqrelid
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'`).Scan(&sequences).Error; err != nil {
		return fmt.Errorf("inspect schema sequences: %w", err)
	}
	actualSequences := make(map[string]schemaSequence, len(sequences))
	for _, s := range sequences {
		actualSequences[s.Name] = s
	}
	for _, s := range expected.Sequences {
		if got, ok := actualSequences[s.Name]; !ok || !reflect.DeepEqual(got, s) {
			return fmt.Errorf("schema incompatible: required sequence %s is missing or differs", s.Name)
		}
	}
	return nil
}
