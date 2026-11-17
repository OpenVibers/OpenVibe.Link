//! X's BGRX pixels to I420 (the 4:2:0 YUV H.264 encodes) in one pass: BT.601 limited range in integer math, each 2×2
//! block's chroma from the average of its four pixels. The openh264 crate's own conversion took 19.9 ms for a 1280×800
//! frame on the workstation (2026-11-17), more than the encoding itself; this one is a few milliseconds.

/// Converts `src` (w×h BGRX, tightly packed; w and h even) into the three planes, grown as needed.
pub fn bgrx_to_i420(src: &[u8], w: usize, h: usize, y: &mut Vec<u8>, u: &mut Vec<u8>, v: &mut Vec<u8>) {
    debug_assert!(w % 2 == 0 && h % 2 == 0 && src.len() >= w * h * 4);
    let (cw, ch) = (w / 2, h / 2);
    y.resize(w * h, 0);
    u.resize(cw * ch, 0);
    v.resize(cw * ch, 0);
    for j in 0..ch {
        let top = &src[2 * j * w * 4..(2 * j + 1) * w * 4];
        let bottom = &src[(2 * j + 1) * w * 4..(2 * j + 2) * w * 4];
        let (y0, rest) = y[2 * j * w..(2 * j + 2) * w].split_at_mut(w);
        let y1 = rest;
        let ur = &mut u[j * cw..(j + 1) * cw];
        let vr = &mut v[j * cw..(j + 1) * cw];
        for (i, ((t, b), (uo, vo))) in top.chunks_exact(8).zip(bottom.chunks_exact(8)).zip(ur.iter_mut().zip(vr.iter_mut())).enumerate() {
            let px = |p: &[u8], k: usize| (p[k + 2] as i32, p[k + 1] as i32, p[k] as i32); // r, g, b
            let (ra, ga, ba) = px(t, 0);
            let (rb, gb, bb) = px(t, 4);
            let (rc, gc, bc) = px(b, 0);
            let (rd, gd, bd) = px(b, 4);
            y0[2 * i] = luma(ra, ga, ba);
            y0[2 * i + 1] = luma(rb, gb, bb);
            y1[2 * i] = luma(rc, gc, bc);
            y1[2 * i + 1] = luma(rd, gd, bd);
            let (r, g, b) = ((ra + rb + rc + rd + 2) >> 2, (ga + gb + gc + gd + 2) >> 2, (ba + bb + bc + bd + 2) >> 2);
            *uo = (((-38 * r - 74 * g + 112 * b + 128) >> 8) + 128).clamp(0, 255) as u8;
            *vo = (((112 * r - 94 * g - 18 * b + 128) >> 8) + 128).clamp(0, 255) as u8;
        }
    }
}

#[inline(always)]
fn luma(r: i32, g: i32, b: i32) -> u8 { (((66 * r + 129 * g + 25 * b + 128) >> 8) + 16) as u8 }

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn white_black_and_red_land_where_bt601_puts_them() {
        // 2×2 of one colour each: white, black, pure red.
        for (bgrx, want) in [([255u8, 255, 255, 0], (235u8, 128u8, 128u8)), ([0, 0, 0, 0], (16, 128, 128)), ([0, 0, 255, 0], (82, 90, 240))] {
            let src: Vec<u8> = bgrx.iter().copied().cycle().take(16).collect();
            let (mut y, mut u, mut v) = (Vec::new(), Vec::new(), Vec::new());
            bgrx_to_i420(&src, 2, 2, &mut y, &mut u, &mut v);
            assert_eq!((y[0], u[0], v[0]), want, "{bgrx:?}");
            assert!(y.iter().all(|&p| p == want.0));
        }
    }
}
