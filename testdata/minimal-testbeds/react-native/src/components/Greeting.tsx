import React from "react";
import { Text } from "react-native";

export function Greeting({ title }: { title: string }) {
  return <Text>{title}</Text>;
}
