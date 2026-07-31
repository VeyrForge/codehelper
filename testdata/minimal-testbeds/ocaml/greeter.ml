open Helpers

module Greeter = struct
  let greet name =
    format(name)

  let shout name =
    greet(name)
end
