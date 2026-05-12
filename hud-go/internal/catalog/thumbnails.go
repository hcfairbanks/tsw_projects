package catalog

import (
	"hud-go/internal/pak"
)

// ThumbnailURLPath returns the URL the web UI uses to serve a class's
// rendered thumbnail PNG. The actual file is written under
// <appDir>/images/train_classes/ during the pak catalog scan; this
// helper agrees with that naming so the importer can set
// train_classes.thumbnail_path without checking the file exists.
func ThumbnailURLPath(className string) string {
	return "/images/train_classes/" + pak.SanitiseThumbnailName(className) + ".png"
}
