module Helpers (format) where

format :: String -> String
format s = map toUpper s
  where
    toUpper c
      | c >= 'a' && c <= 'z' = toEnum (fromEnum c - 32)
      | otherwise = c
