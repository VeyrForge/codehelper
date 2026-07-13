use rand::rngs::StdRng;
use rand::SeedableRng;

use crate::types::{Matrix, SubspaceAdapter};
use crate::util::{dot, normalize, sample_standard_normal};

pub fn pca_input_basis(activations: &Matrix, rank: u32, seed: u32) -> Vec<Vec<f32>> {
    let mut ortho: Vec<Vec<f32>> = Vec::with_capacity(rank as usize);
    let mut rng = StdRng::seed_from_u64(seed as u64);

    for _ in 0..rank {
        let mut v = vec![0.0f32; activations.cols as usize];
        for x in &mut v {
            *x = sample_standard_normal(&mut rng);
        }
        normalize(&mut v);

        for _ in 0..20 {
            let mut x_proj = vec![0.0f32; activations.rows as usize];
            for b in 0..activations.rows {
                let mut sum = 0.0f64;
                for j in 0..activations.cols {
                    sum += activations.at(b, j) as f64 * v[j as usize] as f64;
                }
                x_proj[b as usize] = sum as f32;
            }
            let mut v_new = vec![0.0f32; activations.cols as usize];
            for j in 0..activations.cols {
                let mut sum = 0.0f64;
                for b in 0..activations.rows {
                    sum += activations.at(b, j) as f64 * x_proj[b as usize] as f64;
                }
                v_new[j as usize] = (sum / activations.rows as f64) as f32;
            }
            for prev in &ortho {
                let proj = dot(&v_new, prev);
                for (i, x) in v_new.iter_mut().enumerate() {
                    *x -= proj * prev[i];
                }
            }
            v = v_new;
            normalize(&mut v);
        }
        ortho.push(v);
    }
    ortho
}

pub fn solve_symmetric(matrix: &[f64], rhs: &[f64], n: u32) -> Vec<f32> {
    let n = n as usize;
    let mut a = matrix.to_vec();
    let mut b = rhs.to_vec();

    for col in 0..n {
        let mut pivot = col;
        let mut max_abs = a[col * n + col].abs();
        for row in col + 1..n {
            let value = a[row * n + col].abs();
            if value > max_abs {
                max_abs = value;
                pivot = row;
            }
        }
        if max_abs < 1e-12 {
            continue;
        }
        if pivot != col {
            for j in 0..n {
                a.swap(pivot * n + j, col * n + j);
            }
            b.swap(pivot, col);
        }
        let diag = a[col * n + col];
        for j in col..n {
            a[col * n + j] /= diag;
        }
        b[col] /= diag;
        for row in 0..n {
            if row == col {
                continue;
            }
            let factor = a[row * n + col];
            if factor.abs() < 1e-20 {
                continue;
            }
            for j in col..n {
                a[row * n + j] -= factor * a[col * n + j];
            }
            b[row] -= factor * b[col];
        }
    }

    b.iter().map(|&x| x as f32).collect()
}

pub fn fit_subspace_adapter(
    activations: &Matrix,
    target: &Matrix,
    approx: &Matrix,
    rank: u32,
) -> SubspaceAdapter {
    if rank == 0 || activations.rows == 0 {
        return SubspaceAdapter::default();
    }
    let mut adapter = SubspaceAdapter {
        in_dim: activations.cols,
        out_dim: target.cols,
        rank,
        basis: Vec::new(),
        coeff: Vec::new(),
    };
    let bases = pca_input_basis(activations, rank, 17);
    adapter.basis = vec![0.0; rank as usize * activations.cols as usize];
    for r in 0..rank as usize {
        for j in 0..activations.cols as usize {
            adapter.basis[r * activations.cols as usize + j] = bases[r][j];
        }
    }

    let mut mtm = vec![0.0f64; rank as usize * rank as usize];
    let mut mtr = vec![0.0f64; rank as usize * target.cols as usize];

    for b in 0..activations.rows {
        let mut proj = vec![0.0f64; rank as usize];
        for r in 0..rank {
            let mut sum = 0.0f64;
            for j in 0..activations.cols {
                sum += activations.at(b, j) as f64
                    * adapter.basis[r as usize * activations.cols as usize + j as usize] as f64;
            }
            proj[r as usize] = sum;
        }
        let out_base = b as usize * target.cols as usize;
        for r in 0..rank as usize {
            for s in 0..rank as usize {
                mtm[r * rank as usize + s] += proj[r] * proj[s];
            }
            for j in 0..target.cols as usize {
                mtr[r * target.cols as usize + j] +=
                    proj[r] * (target.data[out_base + j] - approx.data[out_base + j]) as f64;
            }
        }
    }

    adapter.coeff = vec![0.0; rank as usize * target.cols as usize];
    for j in 0..target.cols as usize {
        let rhs: Vec<f64> = (0..rank as usize)
            .map(|r| mtr[r * target.cols as usize + j as usize])
            .collect();
        let col = solve_symmetric(&mtm, &rhs, rank);
        for r in 0..rank as usize {
            adapter.coeff[r * target.cols as usize + j as usize] = col[r];
        }
    }
    adapter
}

pub fn subspace_runtime_bytes(adapter: &SubspaceAdapter) -> u64 {
    (adapter.basis.len() + adapter.coeff.len()) as u64 * 4
}
