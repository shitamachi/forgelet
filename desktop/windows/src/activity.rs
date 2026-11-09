use std::{collections::VecDeque, io::Write, path::Path};

pub const LOG_BYTES: usize = 1024 * 1024;
pub const PREVIEW_BYTES: usize = 16 * 1024;

pub struct Tail {
    entries: VecDeque<String>,
    bytes: usize,
    limit: usize,
    truncated: bool,
    revision: u64,
}

impl Tail {
    pub fn new(limit: usize) -> Self {
        Self {
            entries: VecDeque::new(),
            bytes: 0,
            limit,
            truncated: false,
            revision: 0,
        }
    }

    pub fn revision(&self) -> u64 {
        self.revision
    }

    pub fn push(&mut self, entry: String) {
        if entry.is_empty() {
            return;
        }
        self.truncated |= entry.len() > self.limit;
        let entry = suffix(&entry, self.limit);
        self.bytes += entry.len();
        self.entries.push_back(entry.into());
        while self.bytes > self.limit {
            if let Some(first) = self.entries.pop_front() {
                self.bytes -= first.len();
                self.truncated = true;
            }
        }
        self.revision += 1;
    }

    pub fn snapshot(&self, limit: usize) -> String {
        let mut output = String::with_capacity(self.bytes.min(limit));
        if self.truncated || self.bytes > limit {
            output.push_str("… 已省略较早的记录 …\n");
        }
        let mut skip = self.bytes.saturating_sub(limit);
        for entry in &self.entries {
            if skip >= entry.len() {
                skip -= entry.len();
                continue;
            }
            output.push_str(suffix(entry, entry.len() - skip));
            skip = 0;
        }
        output
    }
}

fn suffix(text: &str, limit: usize) -> &str {
    let mut start = text.len().saturating_sub(limit);
    while !text.is_char_boundary(start) {
        start += 1;
    }
    &text[start..]
}

pub fn write_report(path: &Path, report: &str) -> std::io::Result<()> {
    std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .open(path)?
        .write_all(report.as_bytes())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn preview_preserves_unicode_and_chunk_boundaries() {
        let mut tail = Tail::new(64);
        tail.push("状态".into());
        tail.push("正常\n".into());
        assert_eq!(tail.snapshot(64), "状态正常\n");
        assert_eq!(tail.snapshot(6), "… 已省略较早的记录 …\n常\n");
    }

    #[test]
    fn export_marks_evicted_history() {
        let mut tail = Tail::new(8);
        tail.push("old\n".into());
        tail.push("new\n".into());
        tail.push("last\n".into());
        assert_eq!(tail.snapshot(64), "… 已省略较早的记录 …\nlast\n");
        assert_eq!(tail.revision(), 3);
    }
}
