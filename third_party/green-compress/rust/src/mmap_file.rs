use std::path::Path;

use crate::error::{fail, Result};

/// Read-only mmap of a file region (Linux). Used for expert weight streaming.
#[cfg(target_os = "linux")]
pub struct MappedFile {
    pub ptr: *const u8,
    pub len: usize,
    _fd: i32,
}

#[cfg(target_os = "linux")]
impl MappedFile {
    pub fn map(path: &Path) -> Result<Self> {
        use std::fs::OpenOptions;

        let file = OpenOptions::new()
            .read(true)
            .open(path)
            .map_err(|e| fail(format!("mmap open {}: {e}", path.display())))?;
        let len = file
            .metadata()
            .map_err(|e| fail(e.to_string()))?
            .len() as usize;
        if len == 0 {
            return Err(fail("mmap file is empty"));
        }
        let fd = file.as_raw_fd();
        let ptr = unsafe {
            libc::mmap(
                std::ptr::null_mut(),
                len,
                libc::PROT_READ,
                libc::MAP_PRIVATE,
                fd,
                0,
            )
        };
        if ptr == libc::MAP_FAILED {
            return Err(fail(format!("mmap failed: {}", path.display())));
        }
        std::mem::forget(file);
        Ok(Self {
            ptr: ptr as *const u8,
            len,
            _fd: fd,
        })
    }

    pub fn as_slice(&self) -> &[u8] {
        unsafe { std::slice::from_raw_parts(self.ptr, self.len) }
    }
}

#[cfg(target_os = "linux")]
impl Drop for MappedFile {
    fn drop(&mut self) {
        if !self.ptr.is_null() && self.len > 0 {
            unsafe {
                libc::munmap(self.ptr as *mut libc::c_void, self.len);
            }
        }
    }
}

#[cfg(not(target_os = "linux"))]
pub struct MappedFile;

#[cfg(not(target_os = "linux"))]
impl MappedFile {
    pub fn map(_path: &Path) -> Result<Self> {
        Err(fail("MappedFile only supported on Linux"))
    }
}

#[cfg(target_os = "linux")]
use std::os::fd::AsRawFd;
