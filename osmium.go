package geolookup

import _ "embed"

//go:embed docker/export-admin.json
var OsmiumExportAdmin []byte

//go:embed docker/export-places.json
var OsmiumExportPlaces []byte
