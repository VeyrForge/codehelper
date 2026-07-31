%% Shared helpers for the erlang stub bed.
-ifndef(HELPERS_HRL).
-define(HELPERS_HRL, true).

format(S) when is_list(S) ->
    string:uppercase(S);
format(S) when is_binary(S) ->
    string:uppercase(binary_to_list(S)).

-endif.
