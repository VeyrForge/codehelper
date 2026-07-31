use std::fmt;

#[derive(Debug)]
pub struct GreenError {
    message: String,
}

impl GreenError {
    pub fn new(message: impl Into<String>) -> Self {
        Self {
            message: message.into(),
        }
    }
}

impl fmt::Display for GreenError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.message)
    }
}

impl std::error::Error for GreenError {}

pub type Result<T> = std::result::Result<T, GreenError>;

pub fn fail(message: impl Into<String>) -> GreenError {
    GreenError::new(message)
}
