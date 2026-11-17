use chrono::{DateTime, FixedOffset, Local};

/// Capture local wall time when a status arrives, including its date and UTC
/// offset so copied records remain clear across midnight and timezone changes.
pub fn now() -> String {
    stamp(Local::now().fixed_offset())
}

fn stamp(time: DateTime<FixedOffset>) -> String {
    time.format("%Y-%m-%d %H:%M:%S %:z").to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn status_time_uses_local_date_and_offset() {
        let instant = DateTime::parse_from_rfc3339("2026-11-17T18:28:10Z").unwrap();
        for (offset, want) in [
            (0, "2026-11-17 18:28:10 +00:00"),
            (8 * 3600, "2026-11-18 02:28:10 +08:00"),
            (-7 * 3600, "2026-11-17 11:28:10 -07:00"),
            (-8 * 3600, "2026-11-17 10:28:10 -08:00"),
            (5 * 3600 + 45 * 60, "2026-11-18 00:13:10 +05:45"),
        ] {
            let local = instant.with_timezone(&FixedOffset::east_opt(offset).unwrap());
            assert_eq!(stamp(local), want);
        }
    }
}
