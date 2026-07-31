package Greeter;
use strict;
use warnings;
use Helpers;

sub greet {
    my ($name) = @_;
    return format($name);
}

sub shout {
    my ($name) = @_;
    return greet($name);
}

1;
