//! Optional SHA-256 verification for package shards.
//!
//! # Wire (`checksums.json` from `pack-model`)
//!
//! Object map: relative path → lowercase hex digest (64 chars, no `sha256:` prefix).
//! Pack includes every entry in `tensor_files` plus `manifest.json`.
//!
//! Per-tensor `checksum` fields in `manifest.json` use the prefixed form `sha256:<hex>` and
//! typically reference the whole shard file (e.g. all dense rows share `dense.gguf`'s digest).

use std::collections::BTreeMap;
use std::fs::{self, File};
use std::io::{self, Read};
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::schema::CHECKSUMS_FILE;

/// Package-level checksum map (`checksums.json`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PackageChecksums {
    #[serde(flatten)]
    pub entries: BTreeMap<String, String>,
}

impl PackageChecksums {
    pub fn get(&self, rel: &str) -> Option<&str> {
        self.entries.get(rel).map(String::as_str)
    }

    pub fn validate_format(&self) -> Result<(), String> {
        for (path, digest) in &self.entries {
            if path.is_empty() {
                return Err("checksums.json contains empty path key".into());
            }
            parse_checksum(digest).ok_or_else(|| {
                format!("checksums.json entry {path:?} has invalid digest {digest:?}")
            })?;
        }
        Ok(())
    }
}

/// Load `checksums.json` from a package root when present.
pub fn load_checksums(package_root: &Path) -> io::Result<Option<PackageChecksums>> {
    let path = package_root.join(CHECKSUMS_FILE);
    if !path.is_file() {
        return Ok(None);
    }
    let raw = fs::read_to_string(&path)?;
    let map: PackageChecksums = serde_json::from_str(&raw)
        .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e.to_string()))?;
    Ok(Some(map))
}

/// Verify a file matches `expected` (`sha256:<hex>` or raw hex).
pub fn verify_file(path: &Path, expected: &str) -> io::Result<bool> {
    let want = parse_checksum(expected).ok_or_else(|| {
        io::Error::new(io::ErrorKind::InvalidInput, "invalid checksum format")
    })?;
    let got = sha256_file(path)?;
    Ok(got == want)
}

/// Verify every `checksums.json` entry whose relative path exists under `package_root`.
/// Missing files are reported as `(rel, absolute_path)` pairs; digest mismatches use
/// [`ChecksumError::Mismatch`].
pub fn verify_package_checksums(
    package_root: &Path,
    checksums: &PackageChecksums,
) -> Result<(), ChecksumError> {
    checksums
        .validate_format()
        .map_err(ChecksumError::Invalid)?;
    for (rel, expected) in &checksums.entries {
        let abs = package_root.join(rel);
        if !abs.is_file() {
            return Err(ChecksumError::Missing {
                path: abs,
                rel: rel.clone(),
            });
        }
        match verify_file(&abs, expected) {
            Ok(true) => {}
            Ok(false) => {
                return Err(ChecksumError::Mismatch {
                    path: abs,
                    expected: expected.clone(),
                });
            }
            Err(e) => return Err(ChecksumError::Io(e.to_string())),
        }
    }
    Ok(())
}

/// Compute SHA-256 hex digest of `path`.
pub fn sha256_file(path: &Path) -> io::Result<String> {
    let mut f = File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = [0u8; 8192];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(format!("{:x}", hasher.finalize()))
}

/// Normalize `sha256:<hex>` or raw hex to lowercase hex.
pub fn parse_checksum(s: &str) -> Option<String> {
    let hex = s.strip_prefix("sha256:").unwrap_or(s);
    if hex.len() != 64 || !hex.chars().all(|c| c.is_ascii_hexdigit()) {
        return None;
    }
    Some(hex.to_ascii_lowercase())
}

/// Errors while verifying package checksums.
#[derive(Debug, PartialEq, Eq)]
pub enum ChecksumError {
    Invalid(String),
    Missing { path: PathBuf, rel: String },
    Mismatch { path: PathBuf, expected: String },
    Io(String),
}

impl std::fmt::Display for ChecksumError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ChecksumError::Invalid(e) => write!(f, "{e}"),
            ChecksumError::Missing { path, .. } => {
                write!(f, "missing checksummed file {}", path.display())
            }
            ChecksumError::Mismatch { path, expected } => {
                write!(
                    f,
                    "checksum mismatch for {} (expected {expected})",
                    path.display()
                )
            }
            ChecksumError::Io(e) => write!(f, "{e}"),
        }
    }
}

impl std::error::Error for ChecksumError {}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use tempfile::{NamedTempFile, TempDir};

    #[test]
    fn sha256_roundtrip() {
        let mut f = NamedTempFile::new().unwrap();
        write!(f, "green-format").unwrap();
        f.flush().unwrap();
        let digest = sha256_file(f.path()).unwrap();
        assert!(verify_file(f.path(), &format!("sha256:{digest}")).unwrap());
        assert!(verify_file(f.path(), &digest).unwrap());
    }

    #[test]
    fn loads_and_verifies_checksums_json() {
        let dir = TempDir::new().unwrap();
        let body = b"dense-bytes";
        fs::write(dir.path().join("dense.gguf"), body).unwrap();
        let digest = sha256_file(&dir.path().join("dense.gguf")).unwrap();
        let json = format!(r#"{{"dense.gguf":"{digest}"}}"#);
        fs::write(dir.path().join(CHECKSUMS_FILE), json).unwrap();
        let map = load_checksums(dir.path()).unwrap().unwrap();
        assert_eq!(map.get("dense.gguf"), Some(digest.as_str()));
        verify_package_checksums(dir.path(), &map).unwrap();
    }
}
