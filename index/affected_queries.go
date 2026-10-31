package index

import (
	"path"
	"strings"
)

// Inventory has no hidden result cap: ambiguity and file precision need all
// definitions. Normalize native Windows paths in SQL before ordering or caps.
// Stable value ordering, never SQLite IDs, resolves location ties.
// The private member map uses stored parent/depth and enclosing type ranges;
// parsers can emit member functions without changing their public symbol kind.
func (s *Store) affectedInventory() ([]AffectedSymbol, map[string]bool, map[AffectedSymbol]bool, error) {
	files := map[string]bool{}
	rows, err := s.db.Query(`SELECT replace(rel_path,'\','/') AS normalized_path FROM files ORDER BY normalized_path`)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		files[p] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err = s.db.Query(`SELECT replace(f.rel_path,'\','/') AS normalized_path,s.name,s.kind,s.language,s.start_line,s.end_line,
 EXISTS (SELECT 1 FROM symbols parent WHERE parent.file_id=s.file_id
  AND parent.language=s.language AND parent.name=s.parent AND parent.depth=s.depth-1
  AND parent.start_line<=s.start_line AND parent.end_line>=s.end_line
  AND parent.kind IN ('class','struct','interface','trait','protocol','impl')),
 EXISTS (SELECT 1 FROM refs r WHERE r.file_id=s.file_id AND r.kind='implements'
  AND r.line BETWEEN s.start_line AND s.end_line)
 FROM symbols s JOIN files f ON f.id=s.file_id ORDER BY normalized_path,s.start_line,s.end_line,s.name,s.kind,s.language`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	out := []AffectedSymbol{}
	members := map[AffectedSymbol]bool{}
	for rows.Next() {
		var v AffectedSymbol
		var member, implements bool
		if err := rows.Scan(&v.Path, &v.Name, &v.Kind, &v.Language, &v.StartLine, &v.EndLine, &member, &implements); err != nil {
			return nil, nil, nil, err
		}
		out = append(out, v)
		if implements || member && (v.Kind == "function" || v.Kind == "method") {
			members[v] = true
		}
	}
	return out, files, members, rows.Err()
}

// A query returns one extra row. capped means the query cannot prove that all
// records were returned. Its caller must report the cap even for visited cycles.
type affectedRef struct {
	Path, Name, Kind string
	Line             int
}

func (s *Store) affectedRefs(name string, langs []string, budget int) ([]affectedRef, bool, error) {
	query := `SELECT DISTINCT replace(f.rel_path,'\','/') AS normalized_path,r.name,r.kind,r.line FROM refs r JOIN files f ON f.id=r.file_id WHERE r.name=?`
	args := []any{name}
	if len(langs) > 0 {
		query += ` AND r.language IN (` + strings.TrimSuffix(strings.Repeat("?,", len(langs)), ",") + `)`
		for _, l := range langs {
			args = append(args, l)
		}
	}
	query += ` ORDER BY normalized_path,r.line,r.kind,r.name`
	if budget > 0 {
		query += ` LIMIT ?`
		args = append(args, budget+1)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []affectedRef{}
	for rows.Next() {
		var r affectedRef
		if err := rows.Scan(&r.Path, &r.Name, &r.Kind, &r.Line); err != nil {
			return nil, false, err
		}
		out = append(out, r)
	}
	return out, budget > 0 && len(out) > budget, rows.Err()
}

type affectedImport struct{ Path, Raw string }

func (s *Store) affectedImporters(target string, budget int) ([]affectedImport, bool, error) {
	// instr/substr use literal strings, so '%' and '_' never become SQL wildcards.
	// Retain the existing raw suffix / basename convention and package directory.
	target = strings.ReplaceAll(target, "\\", "/")
	stem := strings.TrimSuffix(path.Base(target), path.Ext(target))
	dir := path.Dir(target)
	if dir == "." {
		dir = ""
	}
	query := `SELECT DISTINCT replace(f.rel_path,'\','/') AS normalized_path,i.raw_path AS raw_import FROM imports i JOIN files f ON f.id=i.file_id WHERE substr(replace(i.raw_path,'\','/'),-length(?))=? OR (?<>'' AND instr(replace(i.raw_path,'\','/'),?)>0) OR (?<>'' AND substr(replace(i.raw_path,'\','/'),-length(?))=?) ORDER BY normalized_path,raw_import`
	args := []any{target, target, stem, stem, dir, dir, dir}
	if budget > 0 {
		query += ` LIMIT ?`
		args = append(args, budget+1)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []affectedImport{}
	for rows.Next() {
		var r affectedImport
		if err := rows.Scan(&r.Path, &r.Raw); err != nil {
			return nil, false, err
		}
		out = append(out, r)
	}
	return out, budget > 0 && len(out) > budget, rows.Err()
}
