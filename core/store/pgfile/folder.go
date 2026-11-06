package pgfile

import (
	"strconv"

	"github.com/jinzhongjia/postgo/core/model"
)

// CollectionInfo is what a collection's root file holds.
type CollectionInfo struct {
	Format     int
	ID         string
	Name       string
	DefaultEnv string
	Config     model.Config
	// Order is the order of the root's children, by file or directory
	// name.
	Order []string
}

// ReadFolder reads a folder's _folder.postgo: its configuration, and the
// order of its children by name. An empty file is a folder with neither.
func ReadFolder(src []byte) (model.Config, []string, *Origin, error) {
	d := parse(src, folderFile)
	cfg := model.Config{Auth: model.Auth{Type: model.AuthInherit}}
	order := d.decodeConfig(&cfg, nil)
	if err := d.err(); err != nil {
		return model.Config{}, nil, nil, err
	}
	return cfg, order, newOrigin(d, head{}, folderSpecs(cfg, order, model.AuthInherit)), nil
}

// WriteFolder writes a folder's _folder.postgo.
func WriteFolder(cfg model.Config, order []string, o *Origin) []byte {
	return write(nil, folderSpecs(cfg, order, model.AuthInherit), o)
}

// ReadCollection reads a collection's _collection.postgo. A collection in
// a newer format returns ErrNewerFormat.
func ReadCollection(src []byte) (CollectionInfo, *Origin, error) {
	d := parse(src, collectionFile)
	ci := CollectionInfo{Config: model.Config{Auth: model.Auth{Type: model.AuthNone}}}
	ci.Order = d.decodeConfig(&ci.Config, &ci)
	if ci.Format > model.FormatVersion {
		return CollectionInfo{}, nil, ErrNewerFormat
	}
	if err := d.err(); err != nil {
		return CollectionInfo{}, nil, err
	}
	return ci, newOrigin(d, head{}, collectionSpecs(ci)), nil
}

// WriteCollection writes a collection's _collection.postgo, in the format
// of this package.
func WriteCollection(ci CollectionInfo, o *Origin) []byte {
	ci.Format = model.FormatVersion
	return write(nil, collectionSpecs(ci), o)
}

// decodeConfig decodes the sections of a folder or the root, and the
// [postgo] section into ci when it is not nil.
func (d *pdoc) decodeConfig(cfg *model.Config, ci *CollectionInfo) (order []string) {
	d.checkFamilies()
	hasPostgo := false
	for _, sec := range d.sections {
		if !sec.known {
			d.unknown(sec)
			continue
		}
		switch sec.name {
		case "postgo":
			hasPostgo = true
			d.fields(sec, func(k, v string) (bool, error) {
				switch k {
				case "format":
					n, err := strconv.Atoi(v)
					ci.Format = n
					return true, err
				case "id":
					ci.ID = v
				case "name":
					ci.Name = v
				case "default-env":
					ci.DefaultEnv = v
				default:
					return false, nil
				}
				return true, nil
			})
		case "headers":
			cfg.Headers = entKVs(sec.entries)
		case "auth":
			d.decodeAuth(sec, &cfg.Auth)
		case "vars":
			cfg.Vars = entKVs(sec.entries)
		case "script":
			d.decodeScript(sec, &cfg.Scripts)
		case "settings":
			d.decodeSettings(sec, &cfg.Settings, nil)
		case "order":
			for _, p := range sec.entries {
				// An order has nothing to disable: such rows are kept in
				// the file as they are, and mean nothing.
				if !p.e.Disabled {
					order = append(order, p.e.Value)
				}
			}
		case "docs":
			cfg.Docs = blockContent(sec)
		}
	}
	if ci != nil {
		if !hasPostgo {
			d.errorf(1, "a collection's root file has a [postgo] section")
		} else if ci.Format == 0 {
			d.errorf(1, "[postgo] needs its format, as format = 1")
		}
	}
	return order
}

func folderSpecs(cfg model.Config, order []string, defAuth model.AuthType) []spec {
	var specs []spec
	add := func(s spec, ok bool) {
		if ok {
			specs = append(specs, s)
		}
	}
	if len(cfg.Headers) > 0 {
		specs = append(specs, spec{name: "headers", mode: modeHeaderList, entries: kvEnts(cfg.Headers)})
	}
	add(authSpec(cfg.Auth, defAuth))
	add(kvSpec("vars", cfg.Vars))
	specs = append(specs, scriptSpecs(cfg.Scripts)...)
	add(settingsSpec(cfg.Settings, ""))
	if len(order) > 0 {
		ents := make([]ent, len(order))
		for i, n := range order {
			ents[i] = ent{Value: n}
		}
		specs = append(specs, spec{name: "order", mode: modeList, entries: ents})
	}
	add(rawSpec("docs", "", "md", cfg.Docs), cfg.Docs != "")
	return specs
}

func collectionSpecs(ci CollectionInfo) []spec {
	pg := spec{name: "postgo", mode: modeFields, entries: fieldEnts(
		"format", strconv.Itoa(ci.Format), "id", ci.ID, "name", ci.Name, "default-env", ci.DefaultEnv)}
	return append([]spec{pg}, folderSpecs(ci.Config, ci.Order, model.AuthNone)...)
}

// ReadEnvironment reads an environment's file. id is the environment's
// ID, its file name without .postgo.
func ReadEnvironment(id string, src []byte) (*model.Environment, *Origin, error) {
	d := parse(src, envFile)
	e := &model.Environment{ID: id}
	d.checkFamilies()
	for _, sec := range d.sections {
		if !sec.known {
			d.unknown(sec)
			continue
		}
		switch sec.name {
		case "meta":
			d.fields(sec, func(k, v string) (bool, error) {
				switch k {
				case "name":
					e.Name = v
				case "color":
					e.Color = v
				default:
					return false, nil
				}
				return true, nil
			})
		case "vars":
			e.Vars = entKVs(sec.entries)
		case "secrets":
			for _, p := range sec.entries {
				e.Secrets = append(e.Secrets, model.Secret{Name: p.e.Value, Desc: p.e.Desc, Disabled: p.e.Disabled})
			}
		}
	}
	if err := d.err(); err != nil {
		return nil, nil, err
	}
	return e, newOrigin(d, head{}, envSpecs(e)), nil
}

// WriteEnvironment writes an environment's file.
func WriteEnvironment(e *model.Environment, o *Origin) []byte {
	return write(nil, envSpecs(e), o)
}

func envSpecs(e *model.Environment) []spec {
	var specs []spec
	if meta := fieldEnts("name", e.Name, "color", e.Color); len(meta) > 0 {
		specs = append(specs, spec{name: "meta", mode: modeFields, entries: meta})
	}
	if s, ok := kvSpec("vars", e.Vars); ok {
		specs = append(specs, s)
	}
	if len(e.Secrets) > 0 {
		ents := make([]ent, len(e.Secrets))
		for i, s := range e.Secrets {
			ents[i] = ent{Value: s.Name, Desc: s.Desc, Disabled: s.Disabled}
		}
		specs = append(specs, spec{name: "secrets", mode: modeList, entries: ents})
	}
	return specs
}
