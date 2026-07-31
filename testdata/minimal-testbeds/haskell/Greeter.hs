module Greeter where

import Helpers (format)

greet :: String -> String
greet name = format(name)

shout :: String -> String
shout name = greet(name)
