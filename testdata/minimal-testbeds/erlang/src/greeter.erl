-module(greeter).
-export([greet/1, shout/1]).
-include("helpers.hrl").

greet(Name) ->
    format(Name).

shout(Name) ->
    greet(Name).
