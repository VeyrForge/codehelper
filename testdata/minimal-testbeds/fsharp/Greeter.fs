module Greeter

open Helpers

type Greeter() =
    member _.Greet(name: string) =
        format(name)

    member _.Shout(name: string) =
        this.Greet(name)
