//! World-to-geographic coordinate conversion for TSW6: standard UTM
//! (WGS84, k₀=0.9996, false easting 500,000). Mirrors hud-go's
//! `internal/geo` package — `utm.go`, `arc.go`, `origin.go`,
//! `country.go` — line-for-line so per-route extracts agree on the
//! sub-metre with hud-go's output.

use std::fs;
use std::path::Path;

// =================================================================== WGS84

const A: f64  = 6_378_137.0;             // semi-major axis (m)
const F: f64  = 1.0 / 298.257_223_563;   // flattening
const K0: f64 = 0.9996;                  // UTM scale factor at central meridian
const FE: f64 = 500_000.0;               // UTM false easting (m)

#[inline] fn e2()  -> f64 { F * (2.0 - F) }
#[inline] fn ep2() -> f64 { e2() / (1.0 - e2()) }

/// Return the UTM zone for a given longitude in degrees.
pub fn zone(lng_deg: f64) -> i32 {
    let z = ((lng_deg + 180.0) / 6.0) as i32 + 1;
    z.clamp(1, 60)
}

/// Central meridian longitude (degrees) for a UTM zone.
pub fn central_meridian(zone: i32) -> f64 {
    -177.0 + (zone as f64 - 1.0) * 6.0
}

fn meridional_arc(phi: f64) -> f64 {
    let e2 = e2();
    A * ((1.0 - e2/4.0 - 3.0*e2*e2/64.0 - 5.0*e2.powi(3)/256.0) * phi
        - (3.0*e2/8.0 + 3.0*e2*e2/32.0 + 45.0*e2.powi(3)/1024.0) * (2.0*phi).sin()
        + (15.0*e2*e2/256.0 + 45.0*e2.powi(3)/1024.0) * (4.0*phi).sin()
        - (35.0*e2.powi(3)/3072.0) * (6.0*phi).sin())
}

/// Forward UTM. Returns `(easting, northing)` in metres. Northing is
/// measured from the equator (northern hemisphere convention).
pub fn utm_forward(lat_deg: f64, lng_deg: f64, zone: i32) -> (f64, f64) {
    let cm = central_meridian(zone);
    let b  = lat_deg.to_radians();
    let l  = lng_deg.to_radians();
    let l0 = cm.to_radians();
    let sin_b = b.sin();
    let cos_b = b.cos();
    let tan_b = b.tan();
    let e2 = e2(); let ep2 = ep2();
    let n = A / (1.0 - e2*sin_b*sin_b).sqrt();
    let t = tan_b * tan_b;
    let c = ep2 * cos_b * cos_b;
    let a2 = (l - l0) * cos_b;
    let m = meridional_arc(b);
    let x = K0 * n * (a2
        + (1.0 - t + c) * a2.powi(3) / 6.0
        + (5.0 - 18.0*t + t*t + 72.0*c - 58.0*ep2) * a2.powi(5) / 120.0);
    let y = K0 * (m + n*tan_b*(a2*a2/2.0
        + (5.0 - t + 9.0*c + 4.0*c*c) * a2.powi(4) / 24.0
        + (61.0 - 58.0*t + t*t + 600.0*c - 330.0*ep2) * a2.powi(6) / 720.0));
    (x + FE, y)
}

/// Inverse UTM.
pub fn utm_inverse(easting: f64, northing: f64, zone: i32) -> (f64, f64) {
    let cm = central_meridian(zone);
    let x = easting - FE;
    let y = northing;
    let e2 = e2(); let ep2 = ep2();
    let m = y / K0;
    let mu = m / (A * (1.0 - e2/4.0 - 3.0*e2*e2/64.0 - 5.0*e2.powi(3)/256.0));
    let e1 = (1.0 - (1.0 - e2).sqrt()) / (1.0 + (1.0 - e2).sqrt());
    let phi1 = mu
        + (3.0*e1/2.0 - 27.0*e1.powi(3)/32.0) * (2.0*mu).sin()
        + (21.0*e1*e1/16.0 - 55.0*e1.powi(4)/32.0) * (4.0*mu).sin()
        + (151.0*e1.powi(3)/96.0) * (6.0*mu).sin()
        + (1097.0*e1.powi(4)/512.0) * (8.0*mu).sin();
    let sin_p = phi1.sin();
    let cos_p = phi1.cos();
    let tan_p = phi1.tan();
    let c1 = ep2 * cos_p * cos_p;
    let t1 = tan_p * tan_p;
    let n1 = A / (1.0 - e2*sin_p*sin_p).sqrt();
    let r1 = A * (1.0 - e2) / (1.0 - e2*sin_p*sin_p).powf(1.5);
    let d = x / (n1 * K0);
    let lat = phi1 - (n1*tan_p/r1) * (d*d/2.0
        - (5.0 + 3.0*t1 + 10.0*c1 - 4.0*c1*c1 - 9.0*ep2) * d.powi(4) / 24.0
        + (61.0 + 90.0*t1 + 298.0*c1 + 45.0*t1*t1 - 252.0*ep2 - 3.0*c1*c1) * d.powi(6) / 720.0);
    let lng = cm.to_radians()
        + (d - (1.0 + 2.0*t1 + c1) * d.powi(3) / 6.0
        + (5.0 - 2.0*c1 + 28.0*t1 - 3.0*c1*c1 + 8.0*ep2 + 24.0*t1*t1) * d.powi(5) / 120.0) / cos_p;
    (lat.to_degrees(), lng.to_degrees())
}

// =================================================================== Anchor

/// Per-route origin in geographic + UTM forms.
#[derive(Debug, Clone, Copy)]
pub struct RouteAnchor {
    pub origin_lat: f64,
    pub origin_lng: f64,
    pub zone:       i32,
    pub origin_e:   f64,
    pub origin_n:   f64,
}

impl RouteAnchor {
    pub fn new(origin_lat: f64, origin_lng: f64) -> Self {
        let z = zone(origin_lng);
        let (e, n) = utm_forward(origin_lat, origin_lng, z);
        Self {
            origin_lat, origin_lng,
            zone: z, origin_e: e, origin_n: n,
        }
    }

    /// World-space (metres east of origin, metres south of origin) →
    /// (lat, lng).
    pub fn world_to_lat_lng(&self, world_east_m: f64, world_south_m: f64) -> (f64, f64) {
        utm_inverse(self.origin_e + world_east_m, self.origin_n - world_south_m, self.zone)
    }

    /// Inverse of [`Self::world_to_lat_lng`].
    pub fn lat_lng_to_world(&self, lat_deg: f64, lng_deg: f64) -> (f64, f64) {
        let (e, n) = utm_forward(lat_deg, lng_deg, self.zone);
        (e - self.origin_e, self.origin_n - n)
    }

    /// 1000m tiles, centre of tile (0,0) at origin.
    pub fn tile_center_to_lat_lng(&self, tile_x: i32, tile_y: i32) -> (f64, f64) {
        self.world_to_lat_lng((tile_x as f64) * 1000.0, (tile_y as f64) * 1000.0)
    }

    pub fn tile_and_offset_to_lat_lng(
        &self, tile_x: i32, tile_y: i32,
        within_tile_east_m: f64, within_tile_south_m: f64,
    ) -> (f64, f64) {
        self.world_to_lat_lng(
            (tile_x as f64) * 1000.0 + within_tile_east_m,
            (tile_y as f64) * 1000.0 + within_tile_south_m,
        )
    }
}

// =================================================================== Arc

/// (Δx, Δy) from the start of a circular or straight-line ribbon segment
/// after travelling `length` units. UE4 convention: tangent points along
/// the ribbon, sweep = `-length / radius` (clockwise from above when
/// viewed in UE coords). Radius 0 / non-finite = treat as straight.
pub fn arc_delta(start_x: f64, start_y: f64, tangent_x: f64, tangent_y: f64, radius: f64, length: f64) -> (f64, f64) {
    let tn = tangent_x.hypot(tangent_y);
    if tn == 0.0 { return (0.0, 0.0); }
    let tx = tangent_x / tn;
    let ty = tangent_y / tn;
    if radius == 0.0 || radius.is_infinite() || radius.is_nan() {
        return (length * tx, length * ty);
    }
    let cx = start_x + radius * ty;
    let cy = start_y - radius * tx;
    let sweep = -length / radius;
    let cos_s = sweep.cos();
    let sin_s = sweep.sin();
    let sdx = start_x - cx;
    let sdy = start_y - cy;
    let ex = cx + cos_s * sdx - sin_s * sdy;
    let ey = cy + sin_s * sdx + cos_s * sdy;
    (ex - start_x, ey - start_y)
}

/// Absolute (x, y) of a point `at_len` along the segment.
pub fn arc_point(start_x: f64, start_y: f64, tangent_x: f64, tangent_y: f64, radius: f64, at_len: f64) -> (f64, f64) {
    let (dx, dy) = arc_delta(start_x, start_y, tangent_x, tangent_y, radius, at_len);
    (start_x + dx, start_y + dy)
}

// =================================================================== Origin

/// Scan a persistent-map `.uexp` file for the route's origin lat/lng.
/// Mirrors hud-go's `ExtractOriginFromUExp`: lng at offset P (float32),
/// lat at P+29 (float32). Walks backwards from EOF — the pair is always
/// near the tail. Returns `Err` when no plausible pair is found.
pub fn extract_origin_from_uexp(uexp_path: &Path) -> Result<(f64, f64), String> {
    let data = fs::read(uexp_path).map_err(|e| format!("read {}: {e}", uexp_path.display()))?;
    const GAP: usize = 29;
    if data.len() < GAP + 4 { return Err(format!("{}: too short", uexp_path.display())); }
    let mut i = data.len() - GAP - 4;
    loop {
        let lng_bits = u32::from_le_bytes(data[i..i+4].try_into().unwrap());
        let lat_bits = u32::from_le_bytes(data[i+GAP..i+GAP+4].try_into().unwrap());
        let lng = f32::from_bits(lng_bits) as f64;
        let lat = f32::from_bits(lat_bits) as f64;
        if plausible_lng(lng) && plausible_lat(lat)
            && !(lat.abs() < 1.0 && lng.abs() < 1.0)
        {
            return Ok((lat, lng));
        }
        if i == 0 { break; }
        i -= 1;
    }
    Err(format!("origin lat/lng not found in {}", uexp_path.display()))
}

fn plausible_lat(v: f64) -> bool { v > 25.0 && v < 72.0 }
fn plausible_lng(v: f64) -> bool { v > -180.0 && v < 180.0 && v.abs() > 0.001 }

// =================================================================== Country

/// Infer a TSW6 country code (matching RouteDefinition.Country values)
/// from a route's origin lat/lng. Offline last-resort fallback; "" when
/// no rectangle matches.
pub fn country_from_origin(lat: f64, lng: f64) -> &'static str {
    if lat == 0.0 && lng == 0.0 { return ""; }
    for c in COUNTRY_BOXES {
        if lat >= c.min_lat && lat <= c.max_lat && lng >= c.min_lng && lng <= c.max_lng {
            return c.code;
        }
    }
    ""
}

struct CountryBox {
    code: &'static str,
    min_lat: f64, max_lat: f64,
    min_lng: f64, max_lng: f64,
}

// Order matters: smaller / overlap-prone boxes are checked before the
// larger ones they sit inside. First match wins. List mirrors hud-go's
// `countryBoxes` exactly (and is restricted to countries TSW ships).
static COUNTRY_BOXES: &[CountryBox] = &[
    CountryBox { code: "NL", min_lat: 51.35, max_lat: 53.7,  min_lng:  3.3,    max_lng:  7.1 },
    CountryBox { code: "CH", min_lat: 45.7,  max_lat: 47.9,  min_lng:  5.9,    max_lng: 10.6 },
    CountryBox { code: "AT", min_lat: 46.3,  max_lat: 49.1,  min_lng:  9.5,    max_lng: 17.3 },
    CountryBox { code: "CZ", min_lat: 48.5,  max_lat: 51.1,  min_lng: 12.0,    max_lng: 18.9 },
    CountryBox { code: "UK", min_lat: 49.5,  max_lat: 61.0,  min_lng: -8.5,    max_lng:  2.0 },
    CountryBox { code: "DE", min_lat: 47.2,  max_lat: 55.1,  min_lng:  5.8,    max_lng: 15.1 },
    CountryBox { code: "FR", min_lat: 41.3,  max_lat: 51.2,  min_lng: -5.5,    max_lng:  9.6 },
    CountryBox { code: "IT", min_lat: 35.4,  max_lat: 47.1,  min_lng:  6.6,    max_lng: 18.6 },
    CountryBox { code: "CA", min_lat: 41.7,  max_lat: 70.0,  min_lng: -141.0,  max_lng: -52.6 },
    CountryBox { code: "US", min_lat: 24.0,  max_lat: 49.5,  min_lng: -125.0,  max_lng: -66.0 },
];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn country_basic() {
        // Same cases hud-go pins in country_test.go.
        assert_eq!(country_from_origin(51.393_001_556_4, 0.168_855_994_9), "UK"); // WCML South
        assert_eq!(country_from_origin(40.388_801_574_7, -78.681_297_302_2), "US"); // Horseshoe Curve
        assert_eq!(country_from_origin(51.115_688_324_0, 6.209_702_968_6), "DE"); // TC
        assert_eq!(country_from_origin(48.21, 16.37), "AT"); // Vienna — must hit AT not DE
        assert_eq!(country_from_origin(43.65, -79.38), "CA"); // Toronto
        assert_eq!(country_from_origin(0.0, 0.0), "");
        assert_eq!(country_from_origin(30.0, -30.0), ""); // middle of Atlantic
    }

    #[test]
    fn utm_round_trips_near_boston() {
        // Boston Providence anchor — checked against hud-go's UTM math.
        let a = RouteAnchor::new(42.0, -71.0);
        let (e, n) = a.lat_lng_to_world(42.001, -71.001);
        let (lat, lng) = a.world_to_lat_lng(e, n);
        assert!((lat - 42.001).abs() < 1e-9, "lat round-trip {lat}");
        assert!((lng - -71.001).abs() < 1e-9, "lng round-trip {lng}");
    }

    #[test]
    fn arc_delta_straight() {
        let (dx, dy) = arc_delta(0.0, 0.0, 1.0, 0.0, 0.0, 100.0);
        assert!((dx - 100.0).abs() < 1e-12);
        assert!(dy.abs() < 1e-12);
    }
}
