# mcpd brand assets

The mark extends the hub-and-spoke geometry in
[`web/public/favicon.svg`](../../web/public/favicon.svg). Colors follow the
dashboard tokens in [`web/src/index.css`](../../web/src/index.css).

| Asset | Use |
| --- | --- |
| [banner-light.svg](banner-light.svg) | README header on light backgrounds; 1600 × 440 |
| [banner-dark.svg](banner-dark.svg) | README header on dark backgrounds; 1600 × 440 |
| [logo-light.svg](logo-light.svg) | Transparent symbol and wordmark for light backgrounds; 480 × 160 |
| [logo-dark.svg](logo-dark.svg) | Transparent symbol and wordmark for dark backgrounds; 480 × 160 |

The suffix names the **background**, not the lettering. Use the light variant
on a light surface and the dark variant on a dark surface. Keep the aspect
ratio and leave clear space around the mark.

The wordmark is drawn as paths and needs no font download. Banner captions
use an Arial/Helvetica/sans-serif stack. The SVGs contain no scripts, animation,
embedded raster images, or external resources, and can be edited directly.

The root README uses a `picture` element to select a banner from the reader's
preferred color scheme, with the light banner as its fallback. Both variants
have an opaque background to remain legible if a renderer ignores the media
query. The project name and description also appear as text in the README.

These are brand illustrations, not screenshots of the application. A future
dashboard screenshot should come from a real demo instance and contain no
credentials, customer data, or sensitive infrastructure details.

The CI, release, and Go-version badges in the README read live repository
metadata from Shields.io. The GHCR and support badges are navigation links,
not status claims. No license or coverage badge is included without a
project license or a published coverage report.
