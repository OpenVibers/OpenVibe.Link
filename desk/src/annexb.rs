//! H.264 Annex B split into access units: the Link core's AccessUnits (internal/video/sources.go), in Rust, for a camera
//! program's stream that `ov-desk publish -- <command>` publishes itself (Link v0.78). A new access unit starts at an
//! access unit delimiter, SPS, PPS or SEI after a slice, or at a slice whose first_mb_in_slice is 0. Zeros before a
//! start code belong to it (00 00 00 01) and are dropped from the NAL before it.
use std::io::Read;

pub struct Splitter {
    au: Vec<u8>,
    has_vcl: bool,
    key: bool,
}

impl Splitter {
    pub fn new() -> Splitter { Splitter { au: Vec::with_capacity(256 * 1024), has_vcl: false, key: false } }

    /// One NAL (header onwards). Returns the access unit it closes, when it closes one.
    fn nal(&mut self, n: &[u8], emit: &mut dyn FnMut(&[u8], bool) -> bool) -> bool {
        if n.is_empty() { return true; }
        let typ = n[0] & 0x1f;
        let vcl = typ == 1 || typ == 5;
        let first_mb_zero = n.len() > 1 && n[1] & 0x80 != 0;
        let mut go_on = true;
        if self.has_vcl && (typ == 9 || typ == 7 || typ == 8 || typ == 6 || (vcl && first_mb_zero)) {
            go_on = emit(&self.au, self.key);
            self.au.clear();
            self.has_vcl = false;
            self.key = false;
        }
        self.au.extend_from_slice(&[0, 0, 0, 1]);
        self.au.extend_from_slice(n);
        if vcl { self.has_vcl = true; }
        if typ == 5 { self.key = true; }
        go_on
    }

    /// Reads r to its end (or until emit says false), calling emit with each whole access unit and whether it is a key.
    pub fn run<R: Read>(&mut self, mut r: R, mut emit: impl FnMut(&[u8], bool) -> bool) -> std::io::Result<()> {
        let mut buf: Vec<u8> = Vec::with_capacity(1 << 20);
        let mut chunk = vec![0u8; 1 << 16];
        // start: where the current NAL's header is (after its start code); None before the first start code.
        let mut start: Option<usize> = None;
        let mut scan = 0usize;
        loop {
            let n = r.read(&mut chunk)?;
            if n == 0 {
                if let Some(s) = start {
                    let end = trim_zeros(&buf, s, buf.len());
                    let nal = buf[s..end].to_vec();
                    if !self.nal(&nal, &mut emit) { return Ok(()); }
                }
                if self.has_vcl { emit(&self.au, self.key); }
                return Ok(());
            }
            buf.extend_from_slice(&chunk[..n]);
            // Start codes in the new bytes (two back, for one that straddles the read).
            let mut i = scan.saturating_sub(2).max(start.unwrap_or(0));
            while let Some(off) = buf[i..].iter().position(|&b| b == 1) {
                let one = i + off;
                if one >= 2 && buf[one - 1] == 0 && buf[one - 2] == 0 && start.map(|s| one - 2 >= s).unwrap_or(true) {
                    if let Some(s) = start {
                        let end = trim_zeros(&buf, s, one - 2);
                        if end > s {
                            let nal = buf[s..end].to_vec();
                            if !self.nal(&nal, &mut emit) { return Ok(()); }
                        }
                    }
                    start = Some(one + 1);
                }
                i = one + 1;
            }
            scan = buf.len();
            // Keep only the NAL being read.
            if let Some(s) = start {
                if s > 1 << 20 {
                    buf.drain(..s);
                    scan -= s;
                    start = Some(0);
                }
            } else if buf.len() > 2 {
                let keep = buf.len() - 2;
                buf.drain(..keep);
                scan = buf.len();
            }
        }
    }
}

fn trim_zeros(buf: &[u8], s: usize, mut end: usize) -> usize {
    while end > s && buf[end - 1] == 0 { end -= 1; }
    end
}

#[cfg(test)]
mod tests {
    use super::*;
    fn units(stream: &[u8], read_size: usize) -> Vec<(Vec<u8>, bool)> {
        struct Chunks<'a>(&'a [u8], usize);
        impl<'a> Read for Chunks<'a> {
            fn read(&mut self, out: &mut [u8]) -> std::io::Result<usize> {
                let n = self.0.len().min(self.1).min(out.len());
                out[..n].copy_from_slice(&self.0[..n]);
                self.0 = &self.0[n..];
                Ok(n)
            }
        }
        let mut got = vec![];
        Splitter::new().run(Chunks(stream, read_size), |au, k| { got.push((au.to_vec(), k)); true }).unwrap();
        got
    }

    #[test]
    fn access_units_split_like_the_core() {
        // SPS, PPS, IDR slice (first_mb 0) | P slice (first_mb 0) | AUD, P slice — with 3- and 4-byte start codes.
        let s: Vec<u8> = [
            &[0, 0, 0, 1, 0x67, 0x42, 0x1f][..], &[0, 0, 1, 0x68, 0xce][..], &[0, 0, 0, 1, 0x65, 0x88, 0x84][..],
            &[0, 0, 1, 0x41, 0x9a, 0x01][..], &[0, 0, 0, 1, 0x09, 0xf0][..], &[0, 0, 1, 0x41, 0x9b, 0x02, 0x00][..],
        ].concat();
        for size in [1, 2, 3, 5, 7, 4096] {
            let u = units(&s, size);
            assert_eq!(u.len(), 3, "read size {size}");
            assert!(u[0].1 && !u[1].1 && !u[2].1);
            assert_eq!(u[0].0, [0, 0, 0, 1, 0x67, 0x42, 0x1f, 0, 0, 0, 1, 0x68, 0xce, 0, 0, 0, 1, 0x65, 0x88, 0x84]);
            assert_eq!(u[1].0, [0, 0, 0, 1, 0x41, 0x9a, 0x01]);
            // The trailing zero belongs to nothing; the AUD opens the last unit.
            assert_eq!(u[2].0, [0, 0, 0, 1, 0x09, 0xf0, 0, 0, 0, 1, 0x41, 0x9b, 0x02]);
        }
    }
}
