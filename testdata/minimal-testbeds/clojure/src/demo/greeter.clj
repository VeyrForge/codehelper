(ns demo.greeter
  (:require [demo.helpers :refer [format]]))

(defn greet [name]
  (format name))

(defn shout [name]
  (greet name))
